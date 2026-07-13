package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newNotificationPreferenceRequest(method string, body any) *http.Request {
	req := newRequest(method, "/api/notification-preferences", body)
	return req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{}))
}

func cleanupNotificationPreference(t *testing.T) {
	t.Helper()
	testPool.Exec(context.Background(), `
		DELETE FROM notification_preference
		WHERE workspace_id = $1 AND user_id = $2
	`, testWorkspaceID, testUserID)
}

func setHandlerNotificationPreferences(t *testing.T, preferences map[string]string) {
	t.Helper()
	cleanupNotificationPreference(t)
	prefsJSON, err := json.Marshal(preferences)
	if err != nil {
		t.Fatalf("marshal notification preferences: %v", err)
	}
	if _, err := testHandler.Queries.UpsertNotificationPreference(context.Background(), db.UpsertNotificationPreferenceParams{
		WorkspaceID: parseUUID(testWorkspaceID),
		UserID:      parseUUID(testUserID),
		Preferences: prefsJSON,
	}); err != nil {
		t.Fatalf("UpsertNotificationPreference: %v", err)
	}
	t.Cleanup(func() { cleanupNotificationPreference(t) })
}

func TestUpdateNotificationPreferencesAcceptsInboxMode(t *testing.T) {
	cleanupNotificationPreference(t)
	t.Cleanup(func() { cleanupNotificationPreference(t) })

	for _, mode := range []string{"all", "mentions_only"} {
		t.Run(mode, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.UpdateNotificationPreferences(w, newNotificationPreferenceRequest(http.MethodPut, map[string]any{
				"preferences": map[string]string{
					"inbox_mode":           mode,
					"system_notifications": "all",
				},
			}))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
			}

			pref, err := testHandler.Queries.GetNotificationPreference(context.Background(), db.GetNotificationPreferenceParams{
				WorkspaceID: parseUUID(testWorkspaceID),
				UserID:      parseUUID(testUserID),
			})
			if err != nil {
				t.Fatalf("GetNotificationPreference: %v", err)
			}
			var stored map[string]string
			if err := json.Unmarshal(pref.Preferences, &stored); err != nil {
				t.Fatalf("decode stored preferences: %v", err)
			}
			if stored["inbox_mode"] != mode {
				t.Fatalf("stored inbox_mode = %q, want %q", stored["inbox_mode"], mode)
			}
		})
	}
}

func TestUpdateNotificationPreferencesValidatesValuesPerGroup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		preferences map[string]string
	}{
		{
			name:        "inbox mode rejects muted",
			preferences: map[string]string{"inbox_mode": "muted"},
		},
		{
			name:        "event group rejects mentions-only",
			preferences: map[string]string{"comments": "mentions_only"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler.UpdateNotificationPreferences(w, newNotificationPreferenceRequest(http.MethodPut, map[string]any{
				"preferences": tc.preferences,
			}))

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
		})
	}
}
