package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	webpushinternal "github.com/multica-ai/multica/server/internal/webpush"
)

func TestNewWebPushDispatcherRejectsPartialVAPIDConfiguration(t *testing.T) {
	t.Setenv("VAPID_PUBLIC_KEY", "configured-without-other-values")
	t.Setenv("VAPID_PRIVATE_KEY", "")
	t.Setenv("VAPID_SUBJECT", "")

	dispatcher, err := newWebPushDispatcher(nil, nil)
	if err == nil {
		t.Fatal("partial VAPID configuration returned nil error")
	}
	if dispatcher != nil {
		t.Fatalf("dispatcher = %#v, want nil", dispatcher)
	}
}

func TestNewWebPushDispatcherAllowsIntentionallyDisabledConfiguration(t *testing.T) {
	t.Setenv("VAPID_PUBLIC_KEY", "")
	t.Setenv("VAPID_PRIVATE_KEY", "")
	t.Setenv("VAPID_SUBJECT", "")

	dispatcher, err := newWebPushDispatcher(nil, nil)
	if err != nil {
		t.Fatalf("newWebPushDispatcher: %v", err)
	}
	if dispatcher == nil || dispatcher.Enabled() {
		t.Fatalf("dispatcher = %#v, want non-nil disabled dispatcher", dispatcher)
	}
}

func TestNewWebPushDispatcherOverrideBypassesEnvironment(t *testing.T) {
	t.Setenv("VAPID_PUBLIC_KEY", "invalid")
	t.Setenv("VAPID_PRIVATE_KEY", "invalid")
	t.Setenv("VAPID_SUBJECT", "invalid")
	override := webpushinternal.NewDispatcher(nil, webpushinternal.Config{})

	dispatcher, err := newWebPushDispatcher(nil, override)
	if err != nil {
		t.Fatalf("newWebPushDispatcher: %v", err)
	}
	if dispatcher != override {
		t.Fatal("newWebPushDispatcher did not preserve the injected dispatcher")
	}
}

func TestWebPushRoutesRejectMachineCredentials(t *testing.T) {
	cloudPAT := "mcn_web_push_guard"
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/pat/verify" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Token != cloudPAT {
			http.Error(w, `{"valid":false,"reason":"format_invalid"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":true,"owner_id":"` + testUserID + `","instance_id":"test-instance"}`))
	}))
	defer fleet.Close()

	t.Setenv("MULTICA_CLOUD_FLEET_URL", fleet.URL)
	t.Setenv("VAPID_PUBLIC_KEY", "")
	t.Setenv("VAPID_PRIVATE_KEY", "")
	t.Setenv("VAPID_SUBJECT", "")
	router, _, err := NewRouterWithOptions(
		testPool,
		realtime.NewHub(),
		events.New(),
		analytics.NoopClient{},
		nil,
		RouterOptions{},
	)
	if err != nil {
		t.Fatalf("NewRouterWithOptions: %v", err)
	}
	taskToken := createWebPushTaskToken(t)

	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/web-push/config"},
		{method: http.MethodPut, path: "/api/web-push/subscription"},
		{method: http.MethodDelete, path: "/api/web-push/subscription"},
		{method: http.MethodPost, path: "/api/web-push/test"},
	}
	credentials := []struct {
		name  string
		token string
	}{
		{name: "task token", token: taskToken},
		{name: "machine PAT", token: cloudPAT},
	}

	for _, credential := range credentials {
		for _, route := range routes {
			t.Run(credential.name+" "+route.method+" "+route.path, func(t *testing.T) {
				request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
				request.Header.Set("Authorization", "Bearer "+credential.token)
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403, body = %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func createWebPushTaskToken(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var agentID string
	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT id, runtime_id
		FROM agent
		WHERE workspace_id = $1 AND archived_at IS NULL
		ORDER BY created_at ASC
		LIMIT 1
	`, testWorkspaceID).Scan(&agentID, &runtimeID); err != nil {
		t.Fatalf("find task-token agent: %v", err)
	}
	var taskID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority)
		VALUES ($1, $2, 'running', 0)
		RETURNING id
	`, agentID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("create task-token task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatalf("GenerateAgentTaskToken: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO task_token (token_hash, task_id, agent_id, workspace_id, user_id, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, auth.HashToken(token), taskID, agentID, testWorkspaceID, testUserID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("create task token: %v", err)
	}
	return token
}
