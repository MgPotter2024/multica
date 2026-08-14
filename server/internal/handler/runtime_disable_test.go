package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newAvailabilityTestRuntime(t *testing.T, status string) string {
	t.Helper()
	var runtimeID string
	if err := testPool.QueryRow(t.Context(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, gen_random_uuid()::text, 'Availability Test Runtime', 'local', 'codex', $2,
			'test', '{}'::jsonb, $3, now())
		RETURNING id
	`, testWorkspaceID, status, testUserID).Scan(&runtimeID); err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(t.Context(), `DELETE FROM agent_runtime WHERE id = $1`, runtimeID) })
	return runtimeID
}

func TestDisableAgentRuntimeDrainsCredentialsAndSurvivesHeartbeat(t *testing.T) {
	runtimeID := newAvailabilityTestRuntime(t, "online")
	agentID := createHandlerTestAgent(t, "Disabled Runtime Agent", []byte(`{}`))
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET runtime_id = $1 WHERE id = $2`, runtimeID, agentID); err != nil {
		t.Fatalf("bind agent: %v", err)
	}
	issueID := createTestIssue(t, "disable runtime task", "in_progress", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	var taskID string
	if err := testPool.QueryRow(t.Context(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, started_at)
		VALUES ($1, $2, $3, 'running', 0, now()) RETURNING id
	`, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("create running task: %v", err)
	}
	if _, err := testPool.Exec(t.Context(), `
		INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, now() + interval '1 hour')
	`, "disable-token-"+taskID, taskID, agentID, testWorkspaceID, testUserID); err != nil {
		t.Fatalf("create task token: %v", err)
	}
	staleSnapshot, err := testHandler.Queries.GetAgentRuntime(t.Context(), util.MustParseUUID(runtimeID))
	if err != nil {
		t.Fatalf("load runtime snapshot: %v", err)
	}

	w := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/runtimes/"+runtimeID+"/disable", nil), "runtimeId", runtimeID)
	testHandler.DisableAgentRuntime(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("disable: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response runtimeAvailabilityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.RemainingNonterminalTaskCount != 0 || len(response.CancelledTaskIDs) != 1 || response.CancelledTaskIDs[0] != taskID {
		t.Fatalf("unexpected disable receipt: %+v", response)
	}

	var runtimeStatus, taskStatus string
	var tokenCount int
	if err := testPool.QueryRow(t.Context(), `SELECT status FROM agent_runtime WHERE id = $1`, runtimeID).Scan(&runtimeStatus); err != nil {
		t.Fatalf("load runtime status: %v", err)
	}
	if err := testPool.QueryRow(t.Context(), `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&taskStatus); err != nil {
		t.Fatalf("load task status: %v", err)
	}
	if err := testPool.QueryRow(t.Context(), `SELECT count(*) FROM task_token WHERE task_id = $1`, taskID).Scan(&tokenCount); err != nil {
		t.Fatalf("count task tokens: %v", err)
	}
	if runtimeStatus != "disabled" || taskStatus != "cancelled" || tokenCount != 0 {
		t.Fatalf("runtime=%q task=%q tokens=%d", runtimeStatus, taskStatus, tokenCount)
	}

	scheduler := NewPassthroughHeartbeatScheduler(testHandler.Queries)
	if err := scheduler.Schedule(t.Context(), staleSnapshot); err != nil {
		t.Fatalf("stale heartbeat: %v", err)
	}
	current, err := testHandler.Queries.GetAgentRuntime(t.Context(), util.MustParseUUID(runtimeID))
	if err != nil || current.Status != "disabled" {
		t.Fatalf("stale heartbeat revived runtime: status=%q err=%v", current.Status, err)
	}
	ack, _, err := testHandler.processHeartbeat(t.Context(), current, true)
	if err != nil || ack.Status != "disabled" {
		t.Fatalf("disabled heartbeat ack=%+v err=%v", ack, err)
	}
}

func TestDisabledRuntimeRejectsTaskInsertionAndStart(t *testing.T) {
	runtimeID := newAvailabilityTestRuntime(t, "disabled")
	agentID := createHandlerTestAgent(t, "Disabled Insert Agent", []byte(`{}`))
	if _, err := testPool.Exec(t.Context(), `UPDATE agent SET runtime_id = $1 WHERE id = $2`, runtimeID, agentID); err != nil {
		t.Fatalf("bind agent: %v", err)
	}
	issueID := createTestIssue(t, "disabled insert", "in_progress", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	agentUUID := util.MustParseUUID(agentID)
	runtimeUUID := util.MustParseUUID(runtimeID)
	issueUUID := util.MustParseUUID(issueID)
	expectNoRows := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("%s error=%v, want pgx.ErrNoRows", name, err)
		}
	}

	_, err := testHandler.Queries.CreateAgentTask(t.Context(), db.CreateAgentTaskParams{AgentID: agentUUID, RuntimeID: runtimeUUID, IssueID: issueUUID})
	expectNoRows("CreateAgentTask", err)
	_, err = testHandler.Queries.CreateQuickCreateTask(t.Context(), db.CreateQuickCreateTaskParams{AgentID: agentUUID, RuntimeID: runtimeUUID, Context: []byte(`{}`)})
	expectNoRows("CreateQuickCreateTask", err)
	_, err = testHandler.Queries.CreateDeferredAgentTask(t.Context(), db.CreateDeferredAgentTaskParams{AgentID: agentUUID, RuntimeID: runtimeUUID, IssueID: issueUUID})
	expectNoRows("CreateDeferredAgentTask", err)
	_, err = testHandler.Queries.CreateChatTask(t.Context(), db.CreateChatTaskParams{AgentID: agentUUID, RuntimeID: runtimeUUID})
	expectNoRows("CreateChatTask", err)
	_, err = testHandler.Queries.CreateAutopilotTask(t.Context(), db.CreateAutopilotTaskParams{AgentID: agentUUID, RuntimeID: runtimeUUID})
	expectNoRows("CreateAutopilotTask", err)

	var parentTaskID string
	if err := testPool.QueryRow(t.Context(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, completed_at)
		VALUES ($1, $2, $3, 'failed', 0, now()) RETURNING id
	`, agentID, runtimeID, issueID).Scan(&parentTaskID); err != nil {
		t.Fatalf("create retry parent: %v", err)
	}
	_, err = testHandler.Queries.CreateRetryTask(t.Context(), db.CreateRetryTaskParams{ID: util.MustParseUUID(parentTaskID)})
	expectNoRows("CreateRetryTask", err)

	var dispatchedTaskID string
	if err := testPool.QueryRow(t.Context(), `
		INSERT INTO agent_task_queue (agent_id, runtime_id, issue_id, status, priority, dispatched_at)
		VALUES ($1, $2, $3, 'dispatched', 0, now()) RETURNING id
	`, agentID, runtimeID, issueID).Scan(&dispatchedTaskID); err != nil {
		t.Fatalf("create dispatched task: %v", err)
	}
	_, err = testHandler.Queries.StartAgentTask(t.Context(), util.MustParseUUID(dispatchedTaskID))
	expectNoRows("StartAgentTask", err)

	candidates, err := testHandler.Queries.ListQueuedClaimCandidatesByRuntime(t.Context(), runtimeUUID)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("disabled candidates=%d err=%v", len(candidates), err)
	}
}

var _ = pgtype.UUID{}
