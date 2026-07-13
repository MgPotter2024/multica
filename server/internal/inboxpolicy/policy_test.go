package inboxpolicy

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestShouldSuppress(t *testing.T) {
	for _, tc := range []struct {
		name                string
		preferences         map[string]string
		notificationType    string
		directMemberMention bool
		want                bool
	}{
		{
			name:             "missing mode preserves ordinary delivery",
			preferences:      map[string]string{},
			notificationType: "quick_create_done",
		},
		{
			name:             "all mode preserves ordinary delivery",
			preferences:      map[string]string{"inbox_mode": ModeAll},
			notificationType: "autopilot_paused",
		},
		{
			name:             "legacy event mute still applies outside mentions-only",
			preferences:      map[string]string{"inbox_mode": ModeAll, "comments": "muted"},
			notificationType: "new_comment",
			want:             true,
		},
		{
			name:             "mentions-only suppresses ordinary rows",
			preferences:      map[string]string{"inbox_mode": ModeMentionsOnly},
			notificationType: "quick_create_failed",
			want:             true,
		},
		{
			name:                "mentions-only permits a direct member mention",
			preferences:         map[string]string{"inbox_mode": ModeMentionsOnly},
			notificationType:    "mentioned",
			directMemberMention: true,
		},
		{
			name: "mentions-only direct mention overrides legacy comments mute",
			preferences: map[string]string{
				"inbox_mode": ModeMentionsOnly,
				"comments":   "muted",
			},
			notificationType:    "mentioned",
			directMemberMention: true,
		},
		{
			name:                "mentions-only rejects expanded mentioned rows",
			preferences:         map[string]string{"inbox_mode": ModeMentionsOnly},
			notificationType:    "mentioned",
			directMemberMention: false,
			want:                true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldSuppress(tc.preferences, tc.notificationType, tc.directMemberMention); got != tc.want {
				t.Fatalf("ShouldSuppress() = %t, want %t", got, tc.want)
			}
		})
	}
}

type preferenceReaderStub struct {
	preference db.NotificationPreference
	err        error
}

func (s preferenceReaderStub) GetNotificationPreference(
	context.Context,
	db.GetNotificationPreferenceParams,
) (db.NotificationPreference, error) {
	return s.preference, s.err
}

func TestShouldSuppressForMemberFailsClosed(t *testing.T) {
	t.Run("missing row retains default delivery", func(t *testing.T) {
		suppressed, err := ShouldSuppressForMember(
			context.Background(), preferenceReaderStub{err: pgx.ErrNoRows},
			db.GetNotificationPreferenceParams{}.WorkspaceID,
			db.GetNotificationPreferenceParams{}.UserID,
			"quick_create_done", false,
		)
		if err != nil || suppressed {
			t.Fatalf("missing row: suppressed=%t err=%v, want false nil", suppressed, err)
		}
	})

	t.Run("query error suppresses", func(t *testing.T) {
		suppressed, err := ShouldSuppressForMember(
			context.Background(), preferenceReaderStub{err: errors.New("read failed")},
			db.GetNotificationPreferenceParams{}.WorkspaceID,
			db.GetNotificationPreferenceParams{}.UserID,
			"quick_create_done", false,
		)
		if err == nil || !suppressed {
			t.Fatalf("query error: suppressed=%t err=%v, want true error", suppressed, err)
		}
	})

	t.Run("decode error suppresses", func(t *testing.T) {
		suppressed, err := ShouldSuppressForMember(
			context.Background(), preferenceReaderStub{preference: db.NotificationPreference{Preferences: []byte(`[]`)}},
			db.GetNotificationPreferenceParams{}.WorkspaceID,
			db.GetNotificationPreferenceParams{}.UserID,
			"quick_create_done", false,
		)
		if err == nil || !suppressed {
			t.Fatalf("decode error: suppressed=%t err=%v, want true error", suppressed, err)
		}
	})
}

type memberReaderStub struct {
	member db.Member
	err    error
}

func (s memberReaderStub) GetMemberByUserAndWorkspace(
	context.Context,
	db.GetMemberByUserAndWorkspaceParams,
) (db.Member, error) {
	return s.member, s.err
}

func TestIsActiveMember(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		wantActive bool
		wantError  bool
	}{
		{name: "active member", wantActive: true},
		{name: "removed member", err: pgx.ErrNoRows},
		{name: "lookup error", err: errors.New("membership read failed"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			active, err := IsActiveMember(
				context.Background(), memberReaderStub{err: tc.err},
				db.GetMemberByUserAndWorkspaceParams{}.WorkspaceID,
				db.GetMemberByUserAndWorkspaceParams{}.UserID,
			)
			if active != tc.wantActive || (err != nil) != tc.wantError {
				t.Fatalf("IsActiveMember() = (%t, %v), want active=%t error=%t", active, err, tc.wantActive, tc.wantError)
			}
		})
	}
}
