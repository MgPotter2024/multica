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
	// ErrAgentCreateStatus (ARG-548 review, ADV-6): agents can never mint an
	// issue directly in a review or terminal status; those transitions must go
	// through the gated status paths (deliver for in_review, reviewer gate for
	// done, humans for blocked/cancelled).
	ErrAgentCreateStatus = errors.New("agents can only create issues with status backlog, todo, or in_progress")
	// ErrReviewerNotInReview: the reviewer role grants exactly the
	// in_review -> done transition; done from any other status stays forbidden.
	ErrReviewerNotInReview = errors.New("reviewer agents can only mark issues done from in_review")
	// ErrReviewerSelfReview: implementer != reviewer. A reviewer agent may
	// never approve an issue currently assigned to itself.
	ErrReviewerSelfReview = errors.New("reviewer agents cannot mark issues assigned to themselves as done")
	// ErrReviewerNoDelivery (ARG-548 review, ADV-1): done requires an
	// immutable delivery receipt — a row the real `issue deliver` flow wrote.
	// Without one, any agent could two-step an issue to done by first setting
	// in_review (which every agent may do) and then approving it.
	ErrReviewerNoDelivery = errors.New("reviewer agents can only mark an issue done after a verified delivery receipt exists for it")
	// ErrReviewerOwnDelivery (ARG-548 review, ADV-2): receipt authorship is
	// immutable, unlike the current assignee. A reviewer that has EVER
	// recorded a delivery receipt for the issue (which includes being the
	// latest deliverer) implemented it and cannot approve it, even after
	// unassigning itself.
	ErrReviewerOwnDelivery = errors.New("reviewer agents cannot mark an issue done that they delivered themselves")
	// ErrOrchestratorParentNotOwned: orchestrator capabilities are scoped to
	// sub-issues under an issue currently assigned to that same agent.
	ErrOrchestratorParentNotOwned = errors.New("orchestrator agents can only create or modify sub-issues under an issue assigned to them")
	// ErrOrchestratorParentDepth (ARG-548 review, ADV-7a): the intended flow
	// is root -> staged children, so the parent of an orchestrator-created
	// sub-issue must itself be a top-level issue (depth cap 1).
	ErrOrchestratorParentDepth = errors.New("orchestrator agents can only create sub-issues under a top-level issue, not under another sub-issue")
	// ErrOrchestratorSelfAssign (ARG-548 review, ADV-7b): an orchestrator
	// creating a sub-issue assigned to itself would trigger its own next run —
	// unbounded self-triggering recursion.
	ErrOrchestratorSelfAssign = errors.New("orchestrator agents cannot create sub-issues assigned to themselves")
)

// CreateFacts carries the per-request facts for ValidateCreate. The handler
// computes them; every lookup failure leaves the permissive fields false so
// the gate fails closed.
type CreateFacts struct {
	// HasParent: the request sets parent_issue_id.
	HasParent bool
	// ParentAssignedToActor: the parent exists in the workspace and is
	// currently assigned to the acting agent.
	ParentAssignedToActor bool
	// ParentIsSubIssue: the parent itself has a parent (depth cap, ADV-7a).
	ParentIsSubIssue bool
	// AssigneeIsActor: the request assigns the new issue to the acting agent
	// itself (self-trigger recursion, ADV-7b).
	AssigneeIsActor bool
}

// ValidateCreate gates issue creation by actor. Members are unrestricted.
// Agents may create parentless quick_create issues (unchanged), and an
// orchestrator agent may additionally create a sub-issue when the parent is a
// TOP-LEVEL issue currently assigned to that same agent and the sub-issue is
// not assigned back to the acting orchestrator.
func ValidateCreate(actorType, actorRole, originType string, f CreateFacts) error {
	if actorType != "agent" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(originType), "quick_create") && !f.HasParent {
		return nil
	}
	if actorRole == RoleOrchestrator && f.HasParent {
		if !f.ParentAssignedToActor {
			return ErrOrchestratorParentNotOwned
		}
		if f.ParentIsSubIssue {
			return ErrOrchestratorParentDepth
		}
		if f.AssigneeIsActor {
			return ErrOrchestratorSelfAssign
		}
		return nil
	}
	return ErrAgentIssueCreation
}

// ValidateCreateStatus gates the create-time status by actor (ARG-548 review,
// ADV-6). Members are unrestricted. Agents — every role — may only mint
// issues in backlog, todo, or in_progress; in_review, done, blocked, and
// cancelled require the gated status-transition paths.
func ValidateCreateStatus(actorType, status string) error {
	if actorType != "agent" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "backlog", "todo", "in_progress":
		return nil
	default:
		return ErrAgentCreateStatus
	}
}

// StatusFacts carries the per-issue facts for ValidateStatus. The handler
// computes them; only the reviewer done branch reads the delivery fields, and
// every lookup failure leaves HasDelivery false so the gate fails closed.
type StatusFacts struct {
	// ActorIsAssignee: the issue is currently assigned to the acting agent.
	ActorIsAssignee bool
	// HasDelivery: at least one delivery receipt (issue_delivery_verification
	// row, written only by the real `issue deliver` flow) exists for the issue.
	HasDelivery bool
	// LatestDeliveredByActor: the most recent delivery receipt was recorded by
	// the acting agent.
	LatestDeliveredByActor bool
	// ActorEverDelivered: the acting agent recorded ANY delivery receipt for
	// the issue. Receipt authorship is immutable, so this stays true even
	// after the agent unassigns itself (ADV-2).
	ActorEverDelivered bool
}

// ValidateStatus gates a status write by actor. Members are unrestricted.
// Agents keep today's backlog/todo/in_progress/in_review set; blocked and
// cancelled stay forbidden for every agent. A reviewer agent may set done only
// when ALL hold (ARG-548 review, ADV-1/ADV-2):
//
//  1. the current status is in_review;
//  2. a delivery receipt exists for the issue;
//  3. the latest delivery receipt was recorded by a DIFFERENT agent, and the
//     acting reviewer has never recorded a receipt for the issue;
//  4. the issue's current assignee is not the acting reviewer.
func ValidateStatus(actorType, actorRole, currentStatus, newStatus string, f StatusFacts) error {
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
		if !f.HasDelivery {
			return ErrReviewerNoDelivery
		}
		if f.LatestDeliveredByActor || f.ActorEverDelivered {
			return ErrReviewerOwnDelivery
		}
		if f.ActorIsAssignee {
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
