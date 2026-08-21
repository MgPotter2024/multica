package handler

import (
	"context"
	"strings"

	"github.com/multica-ai/multica/server/internal/issuepolicy"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Ownership facts for the agent role gates (ARG-548 M9/M10). The pure policy
// lives in internal/issuepolicy; these helpers compute the boolean facts the
// policy functions take as parameters. Every lookup failure yields false so
// the gates fail closed.

// issueAssignedToAgent reports whether the issue row is currently assigned to
// the given agent (assignee_type "agent" with a matching assignee_id).
func issueAssignedToAgent(issue db.Issue, agentID string) bool {
	return issue.AssigneeType.Valid && issue.AssigneeType.String == "agent" &&
		issue.AssigneeID.Valid && uuidToString(issue.AssigneeID) == agentID
}

// agentStatusFacts computes issuepolicy.StatusFacts for a status write on
// `issue` by the given actor. The delivery-receipt facts (ARG-548 review,
// ADV-1/ADV-2) are looked up only for a reviewer agent writing "done" so
// every other status write keeps its current query profile; any lookup
// failure leaves HasDelivery false, which the reviewer done branch rejects
// (fail closed).
func (h *Handler) agentStatusFacts(ctx context.Context, issue db.Issue, actorType, actorRole, actorID, newStatus string) issuepolicy.StatusFacts {
	facts := issuepolicy.StatusFacts{ActorIsAssignee: issueAssignedToAgent(issue, actorID)}
	if actorType != "agent" || actorRole != issuepolicy.RoleReviewer ||
		strings.ToLower(strings.TrimSpace(newStatus)) != "done" {
		return facts
	}
	agentUUID, err := util.ParseUUID(actorID)
	if err != nil {
		return facts
	}
	row, err := h.Queries.GetIssueDeliveryReviewFacts(ctx, db.GetIssueDeliveryReviewFactsParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
		AgentID:     agentUUID,
	})
	if err != nil {
		return facts
	}
	facts.HasDelivery = row.HasDelivery
	facts.LatestDeliveredByActor = row.LatestDeliveredByAgent
	facts.ActorEverDelivered = row.AgentEverDelivered
	return facts
}

// issueParentOwnedByActor reports whether the issue's CURRENT parent exists in
// the same workspace and is assigned to the acting agent.
func (h *Handler) issueParentOwnedByActor(ctx context.Context, issue db.Issue, agentID string) bool {
	if !issue.ParentIssueID.Valid {
		return false
	}
	parent, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issue.ParentIssueID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	return issueAssignedToAgent(parent, agentID)
}

// orchestratorHierarchyFact computes issuepolicy's withinOwnedParent for a
// parent_issue_id change on `issue`: the current parent must be assigned to
// the acting agent, and when a new parent is being set (newParentID non-nil)
// that parent must exist in the same workspace and be assigned to the acting
// agent too. Clearing the parent (newParentID nil) only requires the current
// parent to be owned.
func (h *Handler) orchestratorHierarchyFact(ctx context.Context, issue db.Issue, agentID string, newParentID *string) bool {
	if !h.issueParentOwnedByActor(ctx, issue, agentID) {
		return false
	}
	if newParentID == nil {
		return true
	}
	parentUUID, err := util.ParseUUID(*newParentID)
	if err != nil {
		return false
	}
	parent, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          parentUUID,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		return false
	}
	return issueAssignedToAgent(parent, agentID)
}
