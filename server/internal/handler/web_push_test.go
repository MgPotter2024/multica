package handler

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	webpushinternal "github.com/multica-ai/multica/server/internal/webpush"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeWebPushService struct {
	enabled   bool
	publicKey string
	result    webpushinternal.DeliveryResult
	err       error
	userID    string
	calls     int
}

func (s *fakeWebPushService) Enabled() bool { return s.enabled }

func (s *fakeWebPushService) PublicKey() string { return s.publicKey }

func (s *fakeWebPushService) SendTest(_ context.Context, userID string) (webpushinternal.DeliveryResult, error) {
	s.userID = userID
	s.calls++
	return s.result, s.err
}

func withWebPushService(t *testing.T, service WebPushService) {
	t.Helper()
	previous := testHandler.WebPush
	testHandler.WebPush = service
	t.Cleanup(func() { testHandler.WebPush = previous })
}

func handlerPushSubscription(endpoint string) webpushinternal.Subscription {
	privateBytes := bytes.Repeat([]byte{3}, 32)
	x, y := elliptic.P256().ScalarBaseMult(privateBytes)
	publicBytes := elliptic.Marshal(elliptic.P256(), x, y)
	return webpushinternal.Subscription{
		Endpoint: endpoint,
		Keys: webpushinternal.SubscriptionKeys{
			P256dh: base64.RawURLEncoding.EncodeToString(publicBytes),
			Auth:   base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 16)),
		},
	}
}

func TestGetWebPushConfig(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true, publicKey: "public-key"})
	w := httptest.NewRecorder()
	testHandler.GetWebPushConfig(w, newRequest(http.MethodGet, "/api/web-push/config", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	assertJSONEqual(t, w.Body.Bytes(), `{"enabled":true,"public_key":"public-key"}`)
}

func TestPutWebPushSubscriptionValidatesAndRequiresAuth(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})

	t.Run("auth", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/web-push/subscription", nil)
		testHandler.PutWebPushSubscription(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	t.Run("invalid subscription", func(t *testing.T) {
		w := httptest.NewRecorder()
		invalid := handlerPushSubscription("http://127.0.0.1/internal")
		testHandler.PutWebPushSubscription(w, newRequest(http.MethodPut, "/api/web-push/subscription", invalid))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
		}
	})
}

func TestWebPushSubscriptionOwnershipAndUserScopedDelete(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})
	endpoint := "https://updates.push.services.mozilla.com/wpush/v2/handler-reassign"
	_, _ = testPool.Exec(context.Background(), `DELETE FROM web_push_subscription WHERE endpoint = $1`, endpoint)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM web_push_subscription WHERE endpoint = $1`, endpoint)
	})

	put := func(userID string, subscription webpushinternal.Subscription, wantStatus int) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPut, "/api/web-push/subscription", subscription)
		req.Header.Set("X-User-ID", userID)
		testHandler.PutWebPushSubscription(w, req)
		if w.Code != wantStatus {
			t.Fatalf("PUT status = %d, want %d, body = %s", w.Code, wantStatus, w.Body.String())
		}
	}

	first := handlerPushSubscription(endpoint)
	put(testUserID, first, http.StatusOK)
	rows, err := testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("list first user subscriptions: %v", err)
	}
	if len(rows) != 1 || rows[0].Endpoint != endpoint {
		t.Fatalf("first user rows = %+v", rows)
	}
	firstID := rows[0].ID
	rotated := first
	rotated.Keys.Auth = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, 16))
	put(testUserID, rotated, http.StatusOK)
	rows, err = testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("list first user after rotation: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != firstID || rows[0].Auth != rotated.Keys.Auth {
		t.Fatalf("same-user rotation row = %+v", rows)
	}

	var otherUserID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Web Push Other", "web-push-other@example.com").Scan(&otherUserID); err != nil {
		t.Fatalf("create other user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, otherUserID)
	})

	mismatched := rotated
	mismatched.Keys.Auth = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{6}, 16))
	put(otherUserID, mismatched, http.StatusConflict)
	rows, err = testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("list first user after rejected reassignment: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != firstID || rows[0].Auth != rotated.Keys.Auth {
		t.Fatalf("rejected reassignment changed owner row = %+v", rows)
	}

	put(otherUserID, rotated, http.StatusOK)

	rows, err = testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(testUserID))
	if err != nil {
		t.Fatalf("list first user after reassignment: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("first user retained reassigned endpoint: %+v", rows)
	}
	otherRows, err := testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(otherUserID))
	if err != nil {
		t.Fatalf("list second user subscriptions: %v", err)
	}
	if len(otherRows) != 1 || otherRows[0].ID != firstID || otherRows[0].Auth != rotated.Keys.Auth {
		t.Fatalf("reassigned row = %+v", otherRows)
	}

	deleteFor := func(userID string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodDelete, "/api/web-push/subscription", map[string]string{"endpoint": endpoint})
		req.Header.Set("X-User-ID", userID)
		testHandler.DeleteWebPushSubscription(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("DELETE status = %d, body = %s", w.Code, w.Body.String())
		}
	}

	deleteFor(testUserID)
	otherRows, err = testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(otherUserID))
	if err != nil || len(otherRows) != 1 {
		t.Fatalf("cross-user delete affected subscription: rows=%+v err=%v", otherRows, err)
	}
	deleteFor(otherUserID)
	otherRows, err = testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(otherUserID))
	if err != nil || len(otherRows) != 0 {
		t.Fatalf("owner delete failed: rows=%+v err=%v", otherRows, err)
	}
}

func TestWebPushSubscriptionLimitAllowsExistingDeviceRotation(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})
	var userID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Web Push Limit", "web-push-limit@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})

	put := func(subscription webpushinternal.Subscription) int {
		t.Helper()
		w := httptest.NewRecorder()
		req := newRequest(http.MethodPut, "/api/web-push/subscription", subscription)
		req.Header.Set("X-User-ID", userID)
		testHandler.PutWebPushSubscription(w, req)
		return w.Code
	}

	for index := 0; index < maxWebPushSubscriptionsPerUser; index++ {
		subscription := handlerPushSubscription(fmt.Sprintf("https://push.example.com/limit-%d", index))
		if status := put(subscription); status != http.StatusOK {
			t.Fatalf("subscription %d status = %d", index, status)
		}
	}
	if status := put(handlerPushSubscription("https://push.example.com/limit-overflow")); status != http.StatusConflict {
		t.Fatalf("overflow status = %d, want 409", status)
	}

	rotated := handlerPushSubscription("https://push.example.com/limit-0")
	rotated.Keys.Auth = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	if status := put(rotated); status != http.StatusOK {
		t.Fatalf("existing device rotation status = %d, want 200", status)
	}
	rows, err := testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(userID))
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(rows) != maxWebPushSubscriptionsPerUser {
		t.Fatalf("subscription count = %d, want %d", len(rows), maxWebPushSubscriptionsPerUser)
	}
}

func TestWebPushSubscriptionLimitIsAtomic(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})
	var userID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Web Push Concurrent Limit", "web-push-concurrent-limit@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})

	const attempts = maxWebPushSubscriptionsPerUser + 3
	start := make(chan struct{})
	statuses := make(chan int, attempts)
	for index := 0; index < attempts; index++ {
		go func(index int) {
			<-start
			response := httptest.NewRecorder()
			subscription := handlerPushSubscription(fmt.Sprintf("https://push.example.com/concurrent-limit-%d", index))
			request := newRequest(http.MethodPut, "/api/web-push/subscription", subscription)
			request.Header.Set("X-User-ID", userID)
			testHandler.PutWebPushSubscription(response, request)
			statuses <- response.Code
		}(index)
	}
	close(start)

	accepted := 0
	rejected := 0
	for index := 0; index < attempts; index++ {
		switch status := <-statuses; status {
		case http.StatusOK:
			accepted++
		case http.StatusConflict:
			rejected++
		default:
			t.Fatalf("unexpected concurrent PUT status = %d", status)
		}
	}
	if accepted != maxWebPushSubscriptionsPerUser || rejected != attempts-maxWebPushSubscriptionsPerUser {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
	rows, err := testHandler.Queries.ListWebPushSubscriptionsByUser(context.Background(), parseUUID(userID))
	if err != nil {
		t.Fatalf("list subscriptions: %v", err)
	}
	if len(rows) != maxWebPushSubscriptionsPerUser {
		t.Fatalf("subscription count = %d, want %d", len(rows), maxWebPushSubscriptionsPerUser)
	}
}

func TestWebPushSubscriptionCascadesWithUser(t *testing.T) {
	var userID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, "Web Push Cascade", "web-push-cascade@example.com").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	endpoint := "https://push.example.com/cascade"
	subscription := handlerPushSubscription(endpoint)
	if _, err := testHandler.Queries.UpsertWebPushSubscription(context.Background(), db.UpsertWebPushSubscriptionParams{
		UserID:   parseUUID(userID),
		Endpoint: endpoint,
		P256dh:   subscription.Keys.P256dh,
		Auth:     subscription.Keys.Auth,
	}); err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM web_push_subscription WHERE endpoint = $1`, endpoint).Scan(&count); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if count != 0 {
		t.Fatalf("cascade left %d subscriptions", count)
	}
}

func TestSendWebPushTest(t *testing.T) {
	service := &fakeWebPushService{
		enabled: true,
		result:  webpushinternal.DeliveryResult{Sent: 1},
	}
	withWebPushService(t, service)
	w := httptest.NewRecorder()
	testHandler.SendWebPushTest(w, newRequest(http.MethodPost, "/api/web-push/test", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if service.userID != testUserID {
		t.Fatalf("SendTest user = %q, want %q", service.userID, testUserID)
	}
	var result webpushinternal.DeliveryResult
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Sent != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSendWebPushTestRateLimitedPerUser(t *testing.T) {
	service := &fakeWebPushService{enabled: true, result: webpushinternal.DeliveryResult{Sent: 1}}
	withWebPushService(t, service)
	previous := testHandler.WebPushTestRateLimiter
	testHandler.WebPushTestRateLimiter = NewMemoryWebhookRateLimiter(WebhookRateLimit{Limit: 1, Window: time.Minute})
	t.Cleanup(func() { testHandler.WebPushTestRateLimiter = previous })

	first := httptest.NewRecorder()
	testHandler.SendWebPushTest(first, newRequest(http.MethodPost, "/api/web-push/test", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	testHandler.SendWebPushTest(second, newRequest(http.MethodPost, "/api/web-push/test", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429, body = %s", second.Code, second.Body.String())
	}
	if service.calls != 1 {
		t.Fatalf("SendTest calls = %d, want 1", service.calls)
	}
}

func TestSendWebPushTestReportsNoSubscription(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true, err: webpushinternal.ErrNoSubscriptions})
	w := httptest.NewRecorder()
	testHandler.SendWebPushTest(w, newRequest(http.MethodPost, "/api/web-push/test", nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestWebPushEndpointsDisabledWithoutVAPID(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{})

	w := httptest.NewRecorder()
	testHandler.PutWebPushSubscription(w, newRequest(http.MethodPut, "/api/web-push/subscription", handlerPushSubscription("https://push.example.com/disabled")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	testHandler.SendWebPushTest(w, newRequest(http.MethodPost, "/api/web-push/test", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("test status = %d", w.Code)
	}
}

func TestSendWebPushTestReportsDeliveryFailure(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true, err: webpushinternal.ErrDeliveryFailed})
	w := httptest.NewRecorder()
	testHandler.SendWebPushTest(w, newRequest(http.MethodPost, "/api/web-push/test", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestDeleteWebPushSubscriptionRejectsInvalidEndpoint(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})
	w := httptest.NewRecorder()
	testHandler.DeleteWebPushSubscription(w, newRequest(http.MethodDelete, "/api/web-push/subscription", map[string]string{"endpoint": "https://127.0.0.1/private"}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestSendWebPushTestUnexpectedError(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true, err: errors.New("database unavailable")})
	w := httptest.NewRecorder()
	testHandler.SendWebPushTest(w, newRequest(http.MethodPost, "/api/web-push/test", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestPutWebPushSubscriptionRejectsInvalidUserUUID(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})
	w := httptest.NewRecorder()
	req := newRequest(http.MethodPut, "/api/web-push/subscription", handlerPushSubscription("https://push.example.com/user"))
	req.Header.Set("X-User-ID", "not-a-uuid")
	testHandler.PutWebPushSubscription(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestWebPushSubscriptionResponseDoesNotExposeKeys(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})
	endpoint := "https://push.example.com/response"
	_, _ = testPool.Exec(context.Background(), `DELETE FROM web_push_subscription WHERE endpoint = $1`, endpoint)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM web_push_subscription WHERE endpoint = $1`, endpoint)
	})

	w := httptest.NewRecorder()
	subscription := handlerPushSubscription(endpoint)
	testHandler.PutWebPushSubscription(w, newRequest(http.MethodPut, "/api/web-push/subscription", subscription))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte(subscription.Keys.Auth)) || bytes.Contains(w.Body.Bytes(), []byte(subscription.Keys.P256dh)) {
		t.Fatal("subscription response exposed encryption keys")
	}
}

func TestDeleteWebPushSubscriptionIsIdempotent(t *testing.T) {
	withWebPushService(t, &fakeWebPushService{enabled: true})
	w := httptest.NewRecorder()
	testHandler.DeleteWebPushSubscription(w, newRequest(http.MethodDelete, "/api/web-push/subscription", map[string]string{
		"endpoint": "https://push.example.com/missing-" + time.Now().Format("150405.000000000"),
	}))
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
}
