package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type runtimeAvailabilityResponse struct {
	Runtime                       AgentRuntimeResponse `json:"runtime"`
	CancelledTaskCount            int                  `json:"cancelled_task_count"`
	CancelledTaskIDs              []string             `json:"cancelled_task_ids"`
	RemainingNonterminalTaskCount int64                `json:"remaining_nonterminal_task_count"`
}

func (h *Handler) DisableAgentRuntime(w http.ResponseWriter, r *http.Request) {
	h.setAgentRuntimeEnabled(w, r, false)
}

func (h *Handler) EnableAgentRuntime(w http.ResponseWriter, r *http.Request) {
	h.setAgentRuntimeEnabled(w, r, true)
}

func (h *Handler) setAgentRuntimeEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	runtimeID := chi.URLParam(r, "runtimeId")
	runtimeUUID, ok := parseUUIDOrBadRequest(w, runtimeID, "runtime_id")
	if !ok {
		return
	}

	rt, err := h.Queries.GetAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	workspaceID := uuidToString(rt.WorkspaceID)
	member, ok := h.requireWorkspaceMember(w, r, workspaceID, "runtime not found")
	if !ok {
		return
	}
	if !canEditRuntime(member, rt) {
		writeError(w, http.StatusForbidden, "you can only edit your own runtimes")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update runtime")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	locked, err := qtx.LockAgentRuntime(r.Context(), runtimeUUID)
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime not found")
		return
	}
	if !canEditRuntime(member, locked) {
		writeError(w, http.StatusForbidden, "you can only edit your own runtimes")
		return
	}

	params := db.EnableAgentRuntimeParams{ID: runtimeUUID, WorkspaceID: locked.WorkspaceID}
	var cancelled []db.AgentTaskQueue
	var remaining int64
	if enabled {
		rt, err = qtx.EnableAgentRuntime(r.Context(), params)
	} else {
		rt, err = qtx.DisableAgentRuntime(r.Context(), db.DisableAgentRuntimeParams(params))
		if err == nil {
			cancelled, err = qtx.CancelAgentTasksByRuntimeOrAgent(r.Context(), db.CancelAgentTasksByRuntimeOrAgentParams{
				RuntimeIds: []pgtype.UUID{runtimeUUID},
				AgentIds:   []pgtype.UUID{},
			})
		}
		if err == nil && len(cancelled) > 0 {
			taskIDs := make([]pgtype.UUID, len(cancelled))
			for i := range cancelled {
				taskIDs[i] = cancelled[i].ID
			}
			err = qtx.DeleteTaskTokensByTasks(r.Context(), taskIDs)
		}
		if err == nil {
			remaining, err = qtx.CountUndrainedTasksByRuntimeOrAgent(r.Context(), db.CountUndrainedTasksByRuntimeOrAgentParams{
				RuntimeIds: []pgtype.UUID{runtimeUUID},
				AgentIds:   []pgtype.UUID{},
			})
			if err == nil && remaining != 0 {
				writeJSON(w, http.StatusConflict, map[string]any{
					"code":  "runtime_disable_not_drained",
					"error": "runtime still has non-terminal tasks",
				})
				return
			}
		}
	}
	if err != nil {
		slog.Error("set runtime availability failed", "error", err, "runtime_id", runtimeID, "enabled", enabled)
		writeError(w, http.StatusInternalServerError, "failed to update runtime")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update runtime")
		return
	}

	if h.TaskService != nil && len(cancelled) > 0 {
		postCommitCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		h.TaskService.BroadcastCancelledTasks(postCommitCtx, cancelled)
	}
	h.publish(protocol.EventDaemonRegister, workspaceID, "member", uuidToString(member.UserID), map[string]any{"action": "update"})
	cancelledIDs := make([]string, len(cancelled))
	for i := range cancelled {
		cancelledIDs[i] = uuidToString(cancelled[i].ID)
	}
	writeJSON(w, http.StatusOK, runtimeAvailabilityResponse{
		Runtime:                       runtimeToResponse(rt),
		CancelledTaskCount:            len(cancelled),
		CancelledTaskIDs:              cancelledIDs,
		RemainingNonterminalTaskCount: remaining,
	})
}
