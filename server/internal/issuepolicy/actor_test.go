package issuepolicy

import (
	"errors"
	"testing"
)

func TestValidateCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		actorType             string
		actorRole             string
		originType            string
		hasParent             bool
		parentAssignedToActor bool
		wantErr               error
	}{
		{name: "member creates issue", actorType: "member"},
		{name: "member creates child issue", actorType: "member", hasParent: true},
		{name: "quick create creates requested top level issue", actorType: "agent", originType: "quick_create"},
		{name: "quick create cannot create child", actorType: "agent", originType: "quick_create", hasParent: true, wantErr: ErrAgentIssueCreation},
		{name: "issue bound agent cannot create issue", actorType: "agent", wantErr: ErrAgentIssueCreation},
		{name: "reviewer agent cannot create issue", actorType: "agent", actorRole: RoleReviewer, wantErr: ErrAgentIssueCreation},
		{name: "orchestrator creates sub-issue under own parent", actorType: "agent", actorRole: RoleOrchestrator, hasParent: true, parentAssignedToActor: true},
		{name: "orchestrator cannot create sub-issue under foreign parent", actorType: "agent", actorRole: RoleOrchestrator, hasParent: true, wantErr: ErrOrchestratorParentNotOwned},
		{name: "orchestrator cannot create top level issue", actorType: "agent", actorRole: RoleOrchestrator, wantErr: ErrAgentIssueCreation},
		{name: "orchestrator quick create top level still allowed", actorType: "agent", actorRole: RoleOrchestrator, originType: "quick_create"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCreate(test.actorType, test.actorRole, test.originType, test.hasParent, test.parentAssignedToActor)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateCreate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateStatus(t *testing.T) {
	t.Parallel()

	// Non-terminal statuses stay open to every agent regardless of role or facts.
	for _, status := range []string{"backlog", "todo", "in_progress", "in_review"} {
		if err := ValidateStatus("agent", "", "todo", status, false); err != nil {
			t.Errorf("agent status %q rejected: %v", status, err)
		}
		if err := ValidateStatus("agent", RoleReviewer, "todo", status, true); err != nil {
			t.Errorf("reviewer agent status %q rejected: %v", status, err)
		}
	}
	// Terminal statuses stay forbidden for plain agents; members unrestricted.
	for _, status := range []string{"blocked", "done", "cancelled"} {
		if err := ValidateStatus("agent", "", "in_review", status, false); err == nil {
			t.Errorf("agent status %q accepted", status)
		}
		if err := ValidateStatus("member", "", "in_review", status, false); err != nil {
			t.Errorf("member status %q rejected: %v", status, err)
		}
	}
	// blocked/cancelled stay forbidden for reviewer agents too.
	for _, status := range []string{"blocked", "cancelled"} {
		if err := ValidateStatus("agent", RoleReviewer, "in_review", status, false); !errors.Is(err, ErrAgentTerminalStatus) {
			t.Errorf("reviewer status %q error = %v, want ErrAgentTerminalStatus", status, err)
		}
	}
	// Reviewer: exactly in_review -> done on a not-self-assigned issue.
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done", false); err != nil {
		t.Errorf("reviewer in_review -> done rejected: %v", err)
	}
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done", true); !errors.Is(err, ErrReviewerSelfReview) {
		t.Errorf("reviewer self-review error = %v, want ErrReviewerSelfReview", err)
	}
	for _, current := range []string{"todo", "in_progress", "backlog", "done"} {
		if err := ValidateStatus("agent", RoleReviewer, current, "done", false); !errors.Is(err, ErrReviewerNotInReview) {
			t.Errorf("reviewer done from %q error = %v, want ErrReviewerNotInReview", current, err)
		}
	}
	// Orchestrator role gains no status capability.
	if err := ValidateStatus("agent", RoleOrchestrator, "in_review", "done", false); !errors.Is(err, ErrAgentTerminalStatus) {
		t.Errorf("orchestrator done error = %v, want ErrAgentTerminalStatus", err)
	}
}

func TestValidateHierarchyChange(t *testing.T) {
	t.Parallel()

	if err := ValidateHierarchyChange("agent", "", true, false); !errors.Is(err, ErrAgentHierarchyChange) {
		t.Fatalf("agent hierarchy mutation error = %v, want ErrAgentHierarchyChange", err)
	}
	if err := ValidateHierarchyChange("agent", "", false, false); err != nil {
		t.Fatalf("agent unrelated mutation rejected: %v", err)
	}
	if err := ValidateHierarchyChange("member", "", true, false); err != nil {
		t.Fatalf("member hierarchy mutation rejected: %v", err)
	}
	if err := ValidateHierarchyChange("agent", RoleOrchestrator, true, true); err != nil {
		t.Fatalf("orchestrator owned-subtree mutation rejected: %v", err)
	}
	if err := ValidateHierarchyChange("agent", RoleOrchestrator, true, false); !errors.Is(err, ErrOrchestratorParentNotOwned) {
		t.Fatalf("orchestrator foreign-subtree mutation error = %v, want ErrOrchestratorParentNotOwned", err)
	}
	if err := ValidateHierarchyChange("agent", RoleReviewer, true, true); !errors.Is(err, ErrAgentHierarchyChange) {
		t.Fatalf("reviewer hierarchy mutation error = %v, want ErrAgentHierarchyChange", err)
	}
}
