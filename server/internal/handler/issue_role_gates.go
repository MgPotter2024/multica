package handler

import (
	"context"

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
