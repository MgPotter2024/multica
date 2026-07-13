package inboxpolicy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	ModeAll          = "all"
	ModeMentionsOnly = "mentions_only"
)

var notificationTypeGroups = map[string]string{
	"issue_assigned":     "assignments",
	"unassigned":         "assignments",
	"assignee_changed":   "assignments",
	"status_changed":     "status_changes",
	"new_comment":        "comments",
	"mentioned":          "comments",
	"priority_changed":   "updates",
	"start_date_changed": "updates",
	"due_date_changed":   "updates",
	"task_completed":     "agent_activity",
	"task_failed":        "agent_activity",
	"agent_blocked":      "agent_activity",
	"agent_completed":    "agent_activity",
}

type PreferenceReader interface {
	GetNotificationPreference(context.Context, db.GetNotificationPreferenceParams) (db.NotificationPreference, error)
}

type MemberReader interface {
	GetMemberByUserAndWorkspace(context.Context, db.GetMemberByUserAndWorkspaceParams) (db.Member, error)
}

func IsActiveMember(
	ctx context.Context,
	reader MemberReader,
	workspaceID pgtype.UUID,
	userID pgtype.UUID,
) (bool, error) {
	_, err := reader.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      userID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ShouldSuppress decides whether a future Inbox row is eligible for a member.
// Mentions-only mode is authoritative over legacy event-group mutes: its sole
// eligible signal is a direct member mention.
func ShouldSuppress(preferences map[string]string, notificationType string, directMemberMention bool) bool {
	if preferences["inbox_mode"] == ModeMentionsOnly {
		return notificationType != "mentioned" || !directMemberMention
	}

	group, configurable := notificationTypeGroups[notificationType]
	return configurable && preferences[group] == "muted"
}

// ShouldSuppressForMember returns suppressed=true with any preference read or
// decode error so standalone persistence paths fail closed. pgx.ErrNoRows is
// the intentional exception: no stored preference means default delivery.
func ShouldSuppressForMember(
	ctx context.Context,
	reader PreferenceReader,
	workspaceID pgtype.UUID,
	userID pgtype.UUID,
	notificationType string,
	directMemberMention bool,
) (bool, error) {
	pref, err := reader.GetNotificationPreference(ctx, db.GetNotificationPreferenceParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}

	var preferences map[string]string
	if err := json.Unmarshal(pref.Preferences, &preferences); err != nil {
		return true, fmt.Errorf("decode notification preferences: %w", err)
	}
	return ShouldSuppress(preferences, notificationType, directMemberMention), nil
}
