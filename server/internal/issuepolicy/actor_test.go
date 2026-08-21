package issuepolicy

import (
	"errors"
	"testing"
)

func TestValidateCreate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		actorType  string
		actorRole  string
		originType string
		facts      CreateFacts
		wantErr    error
	}{
		{name: "member creates issue", actorType: "member"},
		{name: "member creates child issue", actorType: "member", facts: CreateFacts{HasParent: true}},
		{name: "quick create creates requested top level issue", actorType: "agent", originType: "quick_create"},
		{name: "quick create cannot create child", actorType: "agent", originType: "quick_create", facts: CreateFacts{HasParent: true}, wantErr: ErrAgentIssueCreation},
		{name: "issue bound agent cannot create issue", actorType: "agent", wantErr: ErrAgentIssueCreation},
		{name: "reviewer agent cannot create issue", actorType: "agent", actorRole: RoleReviewer, wantErr: ErrAgentIssueCreation},
		{name: "orchestrator creates sub-issue under own top-level parent", actorType: "agent", actorRole: RoleOrchestrator, facts: CreateFacts{HasParent: true, ParentAssignedToActor: true}},
		{name: "orchestrator cannot create sub-issue under foreign parent", actorType: "agent", actorRole: RoleOrchestrator, facts: CreateFacts{HasParent: true}, wantErr: ErrOrchestratorParentNotOwned},
		{name: "orchestrator cannot create top level issue", actorType: "agent", actorRole: RoleOrchestrator, wantErr: ErrAgentIssueCreation},
		{name: "orchestrator quick create top level still allowed", actorType: "agent", actorRole: RoleOrchestrator, originType: "quick_create"},
		// ARG-548 review, ADV-7a: depth cap 1 — the parent must be top-level.
		{name: "orchestrator cannot create grandchild under owned sub-issue", actorType: "agent", actorRole: RoleOrchestrator, facts: CreateFacts{HasParent: true, ParentAssignedToActor: true, ParentIsSubIssue: true}, wantErr: ErrOrchestratorParentDepth},
		// ARG-548 review, ADV-7b: no self-assigned sub-issues (self-trigger recursion).
		{name: "orchestrator cannot create sub-issue assigned to itself", actorType: "agent", actorRole: RoleOrchestrator, facts: CreateFacts{HasParent: true, ParentAssignedToActor: true, AssigneeIsActor: true}, wantErr: ErrOrchestratorSelfAssign},
		// Ownership is checked before depth: an unowned sub-issue parent yields NotOwned.
		{name: "unowned sub-issue parent reports not-owned first", actorType: "agent", actorRole: RoleOrchestrator, facts: CreateFacts{HasParent: true, ParentIsSubIssue: true, AssigneeIsActor: true}, wantErr: ErrOrchestratorParentNotOwned},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCreate(test.actorType, test.actorRole, test.originType, test.facts)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateCreate() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestValidateCreateStatus(t *testing.T) {
	t.Parallel()

	// Members are unrestricted, including terminal statuses.
	for _, status := range []string{"", "backlog", "todo", "in_progress", "in_review", "done", "blocked", "cancelled"} {
		if err := ValidateCreateStatus("member", status); err != nil {
			t.Errorf("member create status %q rejected: %v", status, err)
		}
	}
	// Agents may only mint backlog/todo/in_progress (empty defaults to todo).
	for _, status := range []string{"", "backlog", "todo", "in_progress"} {
		if err := ValidateCreateStatus("agent", status); err != nil {
			t.Errorf("agent create status %q rejected: %v", status, err)
		}
	}
	// ARG-548 review, ADV-6: in_review, done, blocked, cancelled are rejected
	// at create time for every agent regardless of role.
	for _, status := range []string{"in_review", "done", "blocked", "cancelled", "bogus"} {
		if err := ValidateCreateStatus("agent", status); !errors.Is(err, ErrAgentCreateStatus) {
			t.Errorf("agent create status %q error = %v, want ErrAgentCreateStatus", status, err)
		}
	}
}

// approvableFacts is the fully-permissive fact set for the reviewer done gate:
// a receipt exists, recorded by a different agent, and the reviewer is not the
// current assignee.
var approvableFacts = StatusFacts{HasDelivery: true}

func TestValidateStatus(t *testing.T) {
	t.Parallel()

	// Non-terminal statuses stay open to every agent regardless of role or facts.
	for _, status := range []string{"backlog", "todo", "in_progress", "in_review"} {
		if err := ValidateStatus("agent", "", "todo", status, StatusFacts{}); err != nil {
			t.Errorf("agent status %q rejected: %v", status, err)
		}
		if err := ValidateStatus("agent", RoleReviewer, "todo", status, StatusFacts{ActorIsAssignee: true}); err != nil {
			t.Errorf("reviewer agent status %q rejected: %v", status, err)
		}
	}
	// Terminal statuses stay forbidden for plain agents; members unrestricted.
	for _, status := range []string{"blocked", "done", "cancelled"} {
		if err := ValidateStatus("agent", "", "in_review", status, approvableFacts); err == nil {
			t.Errorf("agent status %q accepted", status)
		}
		if err := ValidateStatus("member", "", "in_review", status, StatusFacts{}); err != nil {
			t.Errorf("member status %q rejected: %v", status, err)
		}
	}
	// blocked/cancelled stay forbidden for reviewer agents too.
	for _, status := range []string{"blocked", "cancelled"} {
		if err := ValidateStatus("agent", RoleReviewer, "in_review", status, approvableFacts); !errors.Is(err, ErrAgentTerminalStatus) {
			t.Errorf("reviewer status %q error = %v, want ErrAgentTerminalStatus", status, err)
		}
	}
	// Reviewer happy path: in_review -> done with a foreign delivery receipt.
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done", approvableFacts); err != nil {
		t.Errorf("reviewer in_review -> done rejected: %v", err)
	}
	// ARG-548 review, ADV-1: no delivery receipt -> rejected. This closes the
	// two-step attack (any agent sets in_review, reviewer sets done).
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done", StatusFacts{}); !errors.Is(err, ErrReviewerNoDelivery) {
		t.Errorf("reviewer done without receipt error = %v, want ErrReviewerNoDelivery", err)
	}
	// ARG-548 review, ADV-2: receipt authorship is immutable — a reviewer that
	// delivered the issue (latest or ever) cannot approve it, even after
	// unassigning itself (ActorIsAssignee false).
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done",
		StatusFacts{HasDelivery: true, LatestDeliveredByActor: true, ActorEverDelivered: true}); !errors.Is(err, ErrReviewerOwnDelivery) {
		t.Errorf("reviewer own latest delivery error = %v, want ErrReviewerOwnDelivery", err)
	}
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done",
		StatusFacts{HasDelivery: true, ActorEverDelivered: true}); !errors.Is(err, ErrReviewerOwnDelivery) {
		t.Errorf("reviewer historical delivery error = %v, want ErrReviewerOwnDelivery", err)
	}
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done",
		StatusFacts{HasDelivery: true, LatestDeliveredByActor: true}); !errors.Is(err, ErrReviewerOwnDelivery) {
		t.Errorf("reviewer latest delivery error = %v, want ErrReviewerOwnDelivery", err)
	}
	// Current-assignee self-review stays forbidden.
	if err := ValidateStatus("agent", RoleReviewer, "in_review", "done",
		StatusFacts{HasDelivery: true, ActorIsAssignee: true}); !errors.Is(err, ErrReviewerSelfReview) {
		t.Errorf("reviewer self-review error = %v, want ErrReviewerSelfReview", err)
	}
	for _, current := range []string{"todo", "in_progress", "backlog", "done"} {
		if err := ValidateStatus("agent", RoleReviewer, current, "done", approvableFacts); !errors.Is(err, ErrReviewerNotInReview) {
			t.Errorf("reviewer done from %q error = %v, want ErrReviewerNotInReview", current, err)
		}
	}
	// Orchestrator role gains no status capability.
	if err := ValidateStatus("agent", RoleOrchestrator, "in_review", "done", approvableFacts); !errors.Is(err, ErrAgentTerminalStatus) {
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
