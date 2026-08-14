package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func assignDeliveryIssue(t *testing.T, issueID, agentID string) {
	t.Helper()
	if _, err := testPool.Exec(t.Context(), `
		UPDATE issue SET assignee_type = 'agent', assignee_id = $1, status = 'in_progress' WHERE id = $2
	`, agentID, issueID); err != nil {
		t.Fatalf("assign delivery issue: %v", err)
	}
}

func deliveryRequest(t *testing.T, issueID, agentID, taskID string, body map[string]any) *http.Request {
	t.Helper()
	req := newRequest(http.MethodPost, "/api/issues/"+issueID+"/deliver", body)
	req = withURLParam(req, "id", issueID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	return req
}

func validDeliveryBody() map[string]any {
	return map[string]any{
		"content":            "Implemented and verified.",
		"local_verification": map[string]any{"command": "go test ./...", "result": "passed"},
		"customer_path":      map[string]any{"status": "passed", "method": "browser", "surface": "/runtimes", "evidence": "disabled state visible"},
	}
}

func TestDeliverIssueIsAtomicAndIdempotent(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Delivery Agent", []byte(`{}`))
	issueID := createTestIssue(t, "verified delivery", "in_progress", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	assignDeliveryIssue(t, issueID, agentID)
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	w := httptest.NewRecorder()
	testHandler.DeliverIssue(w, deliveryRequest(t, issueID, agentID, taskID, validDeliveryBody()))
	if w.Code != http.StatusOK {
		t.Fatalf("deliver: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response issueDeliveryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	if response.Issue.Status != "in_review" || response.Verification.TaskID != taskID || response.Idempotent {
		t.Fatalf("unexpected delivery response: %+v", response)
	}

	var status string
	var comments, receipts int
	if err := testPool.QueryRow(t.Context(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("load issue status: %v", err)
	}
	if err := testPool.QueryRow(t.Context(), `SELECT count(*) FROM comment WHERE issue_id = $1 AND source_task_id = $2`, issueID, taskID).Scan(&comments); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if err := testPool.QueryRow(t.Context(), `SELECT count(*) FROM issue_delivery_verification WHERE issue_id = $1 AND task_id = $2`, issueID, taskID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if status != "in_review" || comments != 1 || receipts != 1 {
		t.Fatalf("status=%q comments=%d receipts=%d", status, comments, receipts)
	}

	w = httptest.NewRecorder()
	testHandler.DeliverIssue(w, deliveryRequest(t, issueID, agentID, taskID, validDeliveryBody()))
	if w.Code != http.StatusOK {
		t.Fatalf("idempotent deliver: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || !response.Idempotent {
		t.Fatalf("idempotent response=%+v err=%v", response, err)
	}
	if err := testPool.QueryRow(t.Context(), `SELECT count(*) FROM comment WHERE issue_id = $1 AND source_task_id = $2`, issueID, taskID).Scan(&comments); err != nil || comments != 1 {
		t.Fatalf("idempotent comment count=%d err=%v", comments, err)
	}
}

func TestDeliverIssueRejectsMissingEvidenceWithoutMutation(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Invalid Delivery Agent", []byte(`{}`))
	issueID := createTestIssue(t, "invalid delivery", "in_progress", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	assignDeliveryIssue(t, issueID, agentID)
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)

	w := httptest.NewRecorder()
	testHandler.DeliverIssue(w, deliveryRequest(t, issueID, agentID, taskID, map[string]any{"content": "claim only"}))
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	var status string
	var comments int
	testPool.QueryRow(t.Context(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	testPool.QueryRow(t.Context(), `SELECT count(*) FROM comment WHERE issue_id = $1 AND source_task_id = $2`, issueID, taskID).Scan(&comments)
	if status != "in_progress" || comments != 0 {
		t.Fatalf("invalid delivery mutated status=%q comments=%d", status, comments)
	}
}

func TestAgentInReviewRequiresDeliveryOnlyWhenRolloutEnabled(t *testing.T) {
	agentID := createHandlerTestAgent(t, "Delivery Gate Agent", []byte(`{}`))
	issueID := createTestIssue(t, "generic delivery bypass", "in_progress", "none")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	assignDeliveryIssue(t, issueID, agentID)
	taskID := createHandlerTestTaskForAgentOnIssue(t, agentID, issueID)
	previous := testHandler.cfg.VerifiedDeliveryRequired
	testHandler.cfg.VerifiedDeliveryRequired = true
	t.Cleanup(func() { testHandler.cfg.VerifiedDeliveryRequired = previous })

	req := newRequest(http.MethodPut, "/api/issues/"+issueID, map[string]any{"status": "in_review"})
	req = withURLParam(req, "id", issueID)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	w := httptest.NewRecorder()
	testHandler.UpdateIssue(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	var status string
	if err := testPool.QueryRow(t.Context(), `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil || status != "in_progress" {
		t.Fatalf("generic gate mutated status=%q err=%v", status, err)
	}
}
