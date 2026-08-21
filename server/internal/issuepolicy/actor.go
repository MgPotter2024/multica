package issuepolicy

import (
	"errors"
	"strings"
)

// Agent-level roles (ARG-548 M9/M10). Stored in agent.role (migration 183);
// the empty string is the default "no extra capability" role. Handlers load
// the role and pass it here together with the ownership facts — these
// functions stay pure so every branch is unit-testable.
const (
	RoleOrchestrator = "orchestrator"
	RoleReviewer     = "reviewer"
)

var (
	ErrAgentIssueCreation   = errors.New("agents cannot create additional issues from an issue-bound run")
	ErrAgentTerminalStatus  = errors.New("agents cannot set blocked, done, or cancelled status")
	ErrAgentHierarchyChange = errors.New("agents cannot create or modify issue hierarchy")
	// ErrReviewerNotInReview: the reviewer role grants exactly the
	// in_review -> done transition; done from any other status stays forbidden.
	ErrReviewerNotInReview = errors.New("reviewer agents can only mark issues done from in_review")
	// ErrReviewerSelfReview: implementer != reviewer. A reviewer agent may
	// never approve an issue currently assigned to itself.
	ErrReviewerSelfReview = errors.New("reviewer agents cannot mark issues assigned to themselves as done")
	// ErrOrchestratorParentNotOwned: orchestrator capabilities are scoped to
	// sub-issues under an issue currently assigned to that same agent.
	ErrOrchestratorParentNotOwned = errors.New("orchestrator agents can only create or modify sub-issues under an issue assigned to them")
)

// ValidateCreate gates issue creation by actor. Members are unrestricted.
// Agents may create parentless quick_create issues (unchanged), and an
// orchestrator agent may additionally create a sub-issue when the parent is
// currently assigned to that same agent (parentAssignedToActor — the handler
// verifies the parent exists in the workspace and computes the fact).
func ValidateCreate(actorType, actorRole, originType string, hasParent, parentAssignedToActor bool) error {
	if actorType != "agent" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(originType), "quick_create") && !hasParent {
		return nil
	}
	if actorRole == RoleOrchestrator && hasParent {
		if parentAssignedToActor {
			return nil
		}
		return ErrOrchestratorParentNotOwned
	}
	return ErrAgentIssueCreation
}

// ValidateStatus gates a status write by actor. Members are unrestricted.
// Agents keep today's backlog/todo/in_progress/in_review set; blocked and
// cancelled stay forbidden for every agent. A reviewer agent may perform
// exactly the in_review -> done transition, and never on an issue whose
// current assignee is that same reviewer (actorIsAssignee).
func ValidateStatus(actorType, actorRole, currentStatus, newStatus string, actorIsAssignee bool) error {
	if actorType != "agent" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(newStatus)) {
	case "backlog", "todo", "in_progress", "in_review":
		return nil
	case "done":
		if actorRole != RoleReviewer {
			return ErrAgentTerminalStatus
		}
		if strings.ToLower(strings.TrimSpace(currentStatus)) != "in_review" {
			return ErrReviewerNotInReview
		}
		if actorIsAssignee {
			return ErrReviewerSelfReview
		}
		return nil
	default:
		return ErrAgentTerminalStatus
	}
}

// ValidateHierarchyChange gates parent-link mutations by actor. Members are
// unrestricted; agents cannot touch hierarchy — except an orchestrator agent
// moving a sub-issue within its own subtree: withinOwnedParent is true only
// when every parent touched by the change (the current parent, and the new
// parent when one is being set) is an issue currently assigned to that same
// agent. The handler supplies the fact.
func ValidateHierarchyChange(actorType, actorRole string, touched, withinOwnedParent bool) error {
	if actorType != "agent" || !touched {
		return nil
	}
	if actorRole == RoleOrchestrator {
		if withinOwnedParent {
			return nil
		}
		return ErrOrchestratorParentNotOwned
	}
	return ErrAgentHierarchyChange
}
