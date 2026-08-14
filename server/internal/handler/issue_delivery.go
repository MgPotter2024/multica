package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	maxDeliveryContentRunes = 40_000
	maxDeliveryFieldRunes   = 2_000
	maxDeliveryCommandRunes = 1_000
)

const firstVerifiedDeliveryCLIVersion = 7

type issueDeliveryLocalVerification struct {
	Command string `json:"command"`
	Result  string `json:"result"`
}

type issueDeliveryCustomerPath struct {
	Status   string `json:"status"`
	Method   string `json:"method,omitempty"`
	Surface  string `json:"surface,omitempty"`
	Evidence string `json:"evidence,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type issueDeliveryRequest struct {
	Content           string                         `json:"content"`
	ParentID          string                         `json:"parent_id,omitempty"`
	LocalVerification issueDeliveryLocalVerification `json:"local_verification"`
	CustomerPath      issueDeliveryCustomerPath      `json:"customer_path"`
}

type issueDeliveryReceipt struct {
	Version           int                            `json:"version"`
	AgentID           string                         `json:"agent_id"`
	TaskID            string                         `json:"task_id"`
	CommentID         string                         `json:"comment_id"`
	RecordedAt        string                         `json:"recorded_at"`
	LocalVerification issueDeliveryLocalVerification `json:"local_verification"`
	CustomerPath      issueDeliveryCustomerPath      `json:"customer_path"`
}

type issueDeliveryResponse struct {
	Issue        IssueResponse        `json:"issue"`
	Comment      CommentResponse      `json:"comment"`
	Verification issueDeliveryReceipt `json:"verification"`
	Idempotent   bool                 `json:"idempotent"`
}

func (h *Handler) verifiedDeliveryRequiredForTask(r *http.Request, actorType string) bool {
	if !h.cfg.VerifiedDeliveryRequired || actorType != "agent" {
		return false
	}
	taskID, err := util.ParseUUID(strings.TrimSpace(r.Header.Get("X-Task-ID")))
	if err != nil {
		return false
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil {
		return false
	}
	runtime, err := h.Queries.GetAgentRuntime(r.Context(), task.RuntimeID)
	if err != nil {
		return false
	}
	var metadata struct {
		CLIVersion string `json:"cli_version"`
	}
	if err := json.Unmarshal(runtime.Metadata, &metadata); err != nil {
		return false
	}
	return supportsVerifiedDeliveryCLI(metadata.CLIVersion)
}

func supportsVerifiedDeliveryCLI(version string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.SplitN(version, "-runmux.", 2)
	if len(parts) != 2 {
		return false
	}
	core := strings.Split(parts[0], ".")
	if len(core) != 3 {
		return false
	}
	major, majorErr := strconv.Atoi(core[0])
	minor, minorErr := strconv.Atoi(core[1])
	patch, patchErr := strconv.Atoi(core[2])
	revision, revisionErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil || patchErr != nil || revisionErr != nil {
		return false
	}
	if major != 0 || minor != 3 || patch != 43 {
		return major > 0 || minor > 3 || (minor == 3 && patch > 43)
	}
	return revision >= firstVerifiedDeliveryCLIVersion
}

// DeliverIssue atomically posts the Agent result, stores bounded verification
// evidence, and moves the owned issue to in_review.
func (h *Handler) DeliverIssue(w http.ResponseWriter, r *http.Request) {
	issueID := chi.URLParam(r, "id")
	issue, ok := h.loadIssueForUser(w, r, issueID)
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req issueDeliveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if msg := validateIssueDeliveryRequest(&req); msg != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"code":  "delivery_verification_invalid",
			"error": msg,
		})
		return
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if actorType != "agent" {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"code":  "agent_delivery_required",
			"error": "issue delivery is available only to an authenticated Agent task",
		})
		return
	}
	taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task_id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to deliver issue")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	lockedIssue, err := qtx.LockIssueForDelivery(r.Context(), db.LockIssueForDeliveryParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	task, err := qtx.LockAgentTaskForDelivery(r.Context(), taskUUID)
	if err != nil || uuidToString(task.AgentID) != actorID || !task.IssueID.Valid || uuidToString(task.IssueID) != uuidToString(issue.ID) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"code":  "delivery_task_mismatch",
			"error": "the authenticated Agent task does not belong to this issue",
		})
		return
	}

	var (
		comment    db.Comment
		receipt    issueDeliveryReceipt
		idempotent bool
	)
	storedReceipt, receiptErr := qtx.GetIssueDeliveryVerificationForTask(r.Context(), db.GetIssueDeliveryVerificationForTaskParams{
		WorkspaceID: issue.WorkspaceID, IssueID: issue.ID, TaskID: task.ID,
	})
	if receiptErr == nil {
		if err := json.Unmarshal(storedReceipt, &receipt); err != nil || receipt.TaskID != taskID || receipt.CommentID == "" {
			writeError(w, http.StatusInternalServerError, "stored delivery receipt is invalid")
			return
		}
		commentUUID, parseErr := util.ParseUUID(receipt.CommentID)
		if parseErr != nil {
			writeError(w, http.StatusInternalServerError, "stored delivery receipt is invalid")
			return
		}
		comment, err = qtx.GetComment(r.Context(), commentUUID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load stored delivery comment")
			return
		}
		idempotent = true
	} else if !errors.Is(receiptErr, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load delivery receipt")
		return
	}

	if !idempotent {
		if task.Status != "running" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"code": "delivery_task_not_running", "error": "the Agent task must be running to deliver this issue", "task_status": task.Status,
			})
			return
		}
		owns, ownershipErr := deliveryTaskOwnsIssue(r.Context(), qtx, task, lockedIssue)
		if ownershipErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize issue delivery")
			return
		}
		if !owns {
			writeJSON(w, http.StatusForbidden, map[string]any{
				"code": "delivery_task_not_owner", "error": "only the current Agent assignee or assigned squad leader may deliver this issue",
			})
			return
		}
		if lockedIssue.Status == "done" || lockedIssue.Status == "cancelled" {
			writeJSON(w, http.StatusConflict, map[string]any{
				"code": "delivery_issue_terminal", "error": "a terminal issue cannot be reopened by Agent delivery", "issue_status": lockedIssue.Status,
			})
			return
		}

		var parentID pgtype.UUID
		if req.ParentID != "" {
			parentID, ok = parseUUIDOrBadRequest(w, req.ParentID, "parent_id")
			if !ok {
				return
			}
			parent, err := qtx.GetComment(r.Context(), parentID)
			if err != nil || uuidToString(parent.IssueID) != uuidToString(issue.ID) {
				writeError(w, http.StatusBadRequest, "invalid parent comment")
				return
			}
		}
		if task.TriggerCommentID.Valid && !taskCoversReplyParent(task, parentID) {
			writeError(w, http.StatusConflict, "set parent_id (--parent) to "+uuidToString(task.TriggerCommentID)+" or a coalesced comment id")
			return
		}

		comment, err = qtx.CreateComment(r.Context(), db.CreateCommentParams{
			IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "agent", AuthorID: task.AgentID,
			Content: req.Content, Type: "comment", ParentID: parentID, SourceTaskID: task.ID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create delivery comment")
			return
		}
		receipt = issueDeliveryReceipt{
			Version: 1, AgentID: actorID, TaskID: taskID, CommentID: uuidToString(comment.ID), RecordedAt: time.Now().UTC().Format(time.RFC3339Nano),
			LocalVerification: req.LocalVerification, CustomerPath: req.CustomerPath,
		}
		receiptJSON, marshalErr := json.Marshal(receipt)
		if marshalErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode delivery receipt")
			return
		}
		if _, err = qtx.CreateIssueDeliveryVerification(r.Context(), db.CreateIssueDeliveryVerificationParams{
			TaskID: task.ID, IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AgentID: task.AgentID, CommentID: comment.ID, Receipt: receiptJSON,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to store delivery receipt")
			return
		}
		lockedIssue, err = qtx.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{
			ID: issue.ID, Status: "in_review", WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update issue status")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to deliver issue")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	issueResp := issueToResponse(lockedIssue, prefix)
	commentResp := commentToResponse(comment, nil, nil)
	if !idempotent {
		h.publish(protocol.EventCommentCreated, workspaceID, "agent", actorID, map[string]any{
			"comment": commentResp, "issue_title": lockedIssue.Title, "issue_assignee_type": textToPtr(lockedIssue.AssigneeType),
			"issue_assignee_id": uuidToPtr(lockedIssue.AssigneeID), "issue_status": lockedIssue.Status,
		})
		h.publish(protocol.EventIssueUpdated, workspaceID, "agent", actorID, map[string]any{
			"issue": issueResp, "status_changed": issue.Status != lockedIssue.Status, "prev_status": issue.Status,
		})
		if h.TaskService != nil {
			h.TaskService.CancelDeferredEscalationsForIssueAgent(r.Context(), issue.ID, task.AgentID)
		}
		slog.Info("issue delivered with verification", "issue_id", issueID, "task_id", taskID, "comment_id", receipt.CommentID)
	}
	writeJSON(w, http.StatusOK, issueDeliveryResponse{Issue: issueResp, Comment: commentResp, Verification: receipt, Idempotent: idempotent})
}

func deliveryTaskOwnsIssue(ctx context.Context, q *db.Queries, task db.AgentTaskQueue, issue db.Issue) (bool, error) {
	if !issue.AssigneeType.Valid || !issue.AssigneeID.Valid {
		return false, nil
	}
	switch issue.AssigneeType.String {
	case "agent":
		return issue.AssigneeID == task.AgentID, nil
	case "squad":
		if !task.IsLeaderTask || !task.SquadID.Valid || task.SquadID != issue.AssigneeID {
			return false, nil
		}
		squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return squad.LeaderID == task.AgentID, nil
	default:
		return false, nil
	}
}

func validateIssueDeliveryRequest(req *issueDeliveryRequest) string {
	req.Content = strings.TrimSpace(sanitizeNullBytes(req.Content))
	req.ParentID = strings.TrimSpace(req.ParentID)
	req.LocalVerification.Command = strings.TrimSpace(req.LocalVerification.Command)
	req.LocalVerification.Result = strings.TrimSpace(req.LocalVerification.Result)
	req.CustomerPath.Status = strings.TrimSpace(req.CustomerPath.Status)
	req.CustomerPath.Method = strings.TrimSpace(req.CustomerPath.Method)
	req.CustomerPath.Surface = strings.TrimSpace(req.CustomerPath.Surface)
	req.CustomerPath.Evidence = strings.TrimSpace(req.CustomerPath.Evidence)
	req.CustomerPath.Reason = strings.TrimSpace(req.CustomerPath.Reason)
	if req.Content == "" {
		return "content is required"
	}
	if utf8.RuneCountInString(req.Content) > maxDeliveryContentRunes {
		return "content is too long"
	}
	if req.LocalVerification.Command == "" || req.LocalVerification.Result == "" {
		return "local_verification.command and local_verification.result are required"
	}
	if utf8.RuneCountInString(req.LocalVerification.Command) > maxDeliveryCommandRunes || utf8.RuneCountInString(req.LocalVerification.Result) > maxDeliveryFieldRunes {
		return "local verification evidence is too long"
	}
	switch req.CustomerPath.Status {
	case "passed":
		if req.CustomerPath.Method != "browser" && req.CustomerPath.Method != "cli" && req.CustomerPath.Method != "api" {
			return "customer_path.method must be browser, cli, or api when status is passed"
		}
		if req.CustomerPath.Surface == "" || req.CustomerPath.Evidence == "" {
			return "customer_path.surface and customer_path.evidence are required when status is passed"
		}
		if req.CustomerPath.Reason != "" {
			return "customer_path.reason is only valid when status is not_applicable"
		}
	case "not_applicable":
		if req.CustomerPath.Reason == "" {
			return "customer_path.reason is required when status is not_applicable"
		}
		if req.CustomerPath.Method != "" || req.CustomerPath.Surface != "" || req.CustomerPath.Evidence != "" {
			return "customer path method, surface, and evidence must be omitted when status is not_applicable"
		}
	default:
		return "customer_path.status must be passed or not_applicable"
	}
	for _, value := range []string{req.CustomerPath.Method, req.CustomerPath.Surface, req.CustomerPath.Evidence, req.CustomerPath.Reason} {
		if utf8.RuneCountInString(value) > maxDeliveryFieldRunes {
			return "customer path evidence is too long"
		}
	}
	return ""
}
