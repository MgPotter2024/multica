package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Handler-level coverage for the ARG-548 agent role gates: orchestrator
// sub-issue creation (M9) and the reviewer in_review -> done transition (M10),
// plus the human-only agent.role write gate. The pure policy branches are
// covered in internal/issuepolicy; these tests lock the wiring — role lookup,
// ownership facts, and the privilege-escalation boundary on UpdateAgent.

// seedRoleGateAgent creates a workspace agent with the given issue-policy role
// and a running task so requests carrying X-Agent-ID/X-Task-ID resolve to a
// genuine agent actor.
func seedRoleGateAgent(t *testing.T, name, role string) (agentID, taskID string) {
	t.Helper()
	ctx := context.Background()
	agentID = createHandlerTestAgent(t, name, nil)
	if _, err := testPool.Exec(ctx, `UPDATE agent SET role = $1 WHERE id = $2`, role, agentID); err != nil {
		t.Fatalf("set agent role: %v", err)
	}
	if err := testPool.QueryRow(ctx,
		`INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, originator_user_id)
		 SELECT $1, runtime_id, 'running', 0, $2 FROM agent WHERE id = $1 RETURNING id`,
		agentID, testUserID,
	).Scan(&taskID); err != nil {
		t.Fatalf("seed acting task: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID) })
	return agentID, taskID
}

// seedRoleGateIssue inserts an issue directly. assigneeAgentID and parentID
// are optional ("" skips the column).
func seedRoleGateIssue(t *testing.T, title, status, assigneeAgentID, parentID string) string {
	t.Helper()
	ctx := context.Background()
	var issueID string
	var assigneeType, assigneeVal, parentVal *string
	if assigneeAgentID != "" {
		at := "agent"
		assigneeType, assigneeVal = &at, &assigneeAgentID
	}
	if parentID != "" {
		parentVal = &parentID
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id,
		                   assignee_type, assignee_id, parent_issue_id, number)
		VALUES ($1, $2, $3, 'medium', 'member', $4, $5, $6, $7,
		        COALESCE((SELECT MAX(number) FROM issue WHERE workspace_id = $1), 0) + 1)
		RETURNING id
	`, testWorkspaceID, title, status, testUserID, assigneeType, assigneeVal, parentVal).Scan(&issueID); err != nil {
		t.Fatalf("create test issue %q: %v", title, err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })
	return issueID
}

func asAgentActor(req *http.Request, agentID, taskID string) *http.Request {
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	return req
}

func TestOrchestratorSubIssueCreate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	orchID, orchTask := seedRoleGateAgent(t, "arg548-orchestrator", "orchestrator")
	plainID, plainTask := seedRoleGateAgent(t, "arg548-plain-agent", "")

	ownParent := seedRoleGateIssue(t, "arg548 parent owned by orchestrator", "in_progress", orchID, "")
	foreignParent := seedRoleGateIssue(t, "arg548 parent owned by plain agent", "in_progress", plainID, "")

	createSub := func(t *testing.T, agentID, taskID, parentID, title string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title":           title,
			"parent_issue_id": parentID,
			"stage":           2,
		})
		testHandler.CreateIssue(w, asAgentActor(req, agentID, taskID))
		if w.Code == http.StatusCreated {
			var created IssueResponse
			if err := json.NewDecoder(w.Body).Decode(&created); err == nil {
				t.Cleanup(func() {
					testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
				})
			}
		}
		return w
	}

	t.Run("orchestrator creates staged sub-issue under own parent", func(t *testing.T) {
		w := createSub(t, orchID, orchTask, ownParent, "arg548 sub under own parent")
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("orchestrator rejected under foreign parent", func(t *testing.T) {
		w := createSub(t, orchID, orchTask, foreignParent, "arg548 sub under foreign parent")
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("plain agent rejected even under issue assigned to itself", func(t *testing.T) {
		w := createSub(t, plainID, plainTask, foreignParent, "arg548 plain agent sub")
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestReviewerDoneTransition(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	reviewerID, reviewerTask := seedRoleGateAgent(t, "arg548-reviewer", "reviewer")
	plainID, plainTask := seedRoleGateAgent(t, "arg548-reviewer-peer", "")

	setStatus := func(t *testing.T, agentID, taskID, issueID, status string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": status})
		req = withURLParam(req, "id", issueID)
		testHandler.UpdateIssue(w, asAgentActor(req, agentID, taskID))
		return w
	}

	t.Run("reviewer moves foreign in_review issue to done", func(t *testing.T) {
		issueID := seedRoleGateIssue(t, "arg548 reviewable issue", "in_review", plainID, "")
		w := setStatus(t, reviewerID, reviewerTask, issueID, "done")
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var status string
		if err := testPool.QueryRow(context.Background(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
			t.Fatalf("load status: %v", err)
		}
		if status != "done" {
			t.Fatalf("status = %q, want done", status)
		}
	})
	t.Run("reviewer rejected on issue assigned to itself", func(t *testing.T) {
		issueID := seedRoleGateIssue(t, "arg548 reviewer self issue", "in_review", reviewerID, "")
		if w := setStatus(t, reviewerID, reviewerTask, issueID, "done"); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("reviewer rejected when issue not in_review", func(t *testing.T) {
		issueID := seedRoleGateIssue(t, "arg548 reviewer todo issue", "todo", plainID, "")
		if w := setStatus(t, reviewerID, reviewerTask, issueID, "done"); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("reviewer still cannot set blocked or cancelled", func(t *testing.T) {
		issueID := seedRoleGateIssue(t, "arg548 reviewer blocked issue", "in_review", plainID, "")
		for _, status := range []string{"blocked", "cancelled"} {
			if w := setStatus(t, reviewerID, reviewerTask, issueID, status); w.Code != http.StatusForbidden {
				t.Fatalf("status %q: expected 403, got %d: %s", status, w.Code, w.Body.String())
			}
		}
	})
	t.Run("plain agent still cannot set done", func(t *testing.T) {
		issueID := seedRoleGateIssue(t, "arg548 plain agent done issue", "in_review", reviewerID, "")
		if w := setStatus(t, plainID, plainTask, issueID, "done"); w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestAgentRoleWriteGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	targetID := createHandlerTestAgent(t, "arg548-role-target", nil)
	actorID, actorTask := seedRoleGateAgent(t, "arg548-role-writer", "")

	updateRole := func(t *testing.T, role any, mutate func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/agents/"+targetID, map[string]any{"role": role})
		req = withURLParam(req, "id", targetID)
		if mutate != nil {
			mutate(req)
		}
		testHandler.UpdateAgent(w, req)
		return w
	}
	loadRole := func(t *testing.T) string {
		t.Helper()
		var role string
		if err := testPool.QueryRow(context.Background(), `SELECT role FROM agent WHERE id = $1`, targetID).Scan(&role); err != nil {
			t.Fatalf("load role: %v", err)
		}
		return role
	}

	t.Run("agent actor cannot change roles", func(t *testing.T) {
		w := updateRole(t, "reviewer", func(r *http.Request) { asAgentActor(r, actorID, actorTask) })
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
		if got := loadRole(t); got != "" {
			t.Fatalf("role = %q, want unchanged \"\"", got)
		}
	})
	t.Run("machine credential cannot change roles", func(t *testing.T) {
		w := updateRole(t, "orchestrator", func(r *http.Request) { r.Header.Set("X-Actor-Source", "task_token") })
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("invalid role value rejected", func(t *testing.T) {
		if w := updateRole(t, "admin", nil); w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
		}
	})
	t.Run("human owner sets and clears role", func(t *testing.T) {
		w := updateRole(t, "reviewer", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp AgentResponse
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode agent: %v", err)
		}
		if resp.Role != "reviewer" {
			t.Fatalf("response role = %q, want reviewer", resp.Role)
		}
		if got := loadRole(t); got != "reviewer" {
			t.Fatalf("persisted role = %q, want reviewer", got)
		}
		// Unchanged echo from a machine credential is a tolerated no-op.
		if w := updateRole(t, "reviewer", func(r *http.Request) { r.Header.Set("X-Actor-Source", "task_token") }); w.Code != http.StatusOK {
			t.Fatalf("no-op echo: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if w := updateRole(t, "", nil); w.Code != http.StatusOK {
			t.Fatalf("clear: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		if got := loadRole(t); got != "" {
			t.Fatalf("cleared role = %q, want \"\"", got)
		}
	})
}
