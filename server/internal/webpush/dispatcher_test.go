package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	testPushUserID      = "11111111-1111-4111-8111-111111111111"
	testPushWorkspaceID = "22222222-2222-4222-8222-222222222222"
	testPushItemID      = "33333333-3333-4333-8333-333333333333"
	testPushIssueID     = "44444444-4444-4444-8444-444444444444"
)

type fakeStore struct {
	mu            sync.Mutex
	preference    db.NotificationPreference
	preferenceErr error
	workspace     db.Workspace
	workspaceErr  error
	subscriptions []db.WebPushSubscription
	listErr       error
	listBlock     <-chan struct{}
	listStarted   chan struct{}
	listStartOnce sync.Once
	deleted       []pgtype.UUID
}

func (s *fakeStore) GetNotificationPreference(_ context.Context, _ db.GetNotificationPreferenceParams) (db.NotificationPreference, error) {
	return s.preference, s.preferenceErr
}

func (s *fakeStore) GetWorkspace(_ context.Context, _ pgtype.UUID) (db.Workspace, error) {
	return s.workspace, s.workspaceErr
}

func (s *fakeStore) ListWebPushSubscriptionsByUser(_ context.Context, _ pgtype.UUID) ([]db.WebPushSubscription, error) {
	if s.listStarted != nil {
		s.listStartOnce.Do(func() { close(s.listStarted) })
	}
	if s.listBlock != nil {
		<-s.listBlock
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]db.WebPushSubscription(nil), s.subscriptions...), s.listErr
}

func (s *fakeStore) DeleteWebPushSubscriptionByID(_ context.Context, id pgtype.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleted = append(s.deleted, id)
	return nil
}

type senderFunc func(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error)

func (f senderFunc) Send(ctx context.Context, payload []byte, subscription *webpushgo.Subscription, options *webpushgo.Options) (*http.Response, error) {
	return f(ctx, payload, subscription, options)
}

func enabledTestConfig(t *testing.T) Config {
	t.Helper()
	publicKey, privateKey := testVAPIDKeys()
	cfg, err := NewConfig(publicKey, privateKey, "mailto:ops@example.com")
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	return cfg
}

func testStoredSubscription() db.WebPushSubscription {
	subscription := testSubscription()
	return db.WebPushSubscription{
		ID:       mustUUID(testPushItemID),
		UserID:   mustUUID(testPushUserID),
		Endpoint: subscription.Endpoint,
		P256dh:   subscription.Keys.P256dh,
		Auth:     subscription.Keys.Auth,
	}
}

func mustUUID(value string) pgtype.UUID {
	uuid, err := util.ParseUUID(value)
	if err != nil {
		panic(err)
	}
	return uuid
}

func testInboxEvent() events.Event {
	issueID := testPushIssueID
	body := "A new comment needs your attention"
	return events.Event{
		Type:        protocol.EventInboxNew,
		WorkspaceID: testPushWorkspaceID,
		Payload: map[string]any{
			"item": map[string]any{
				"id":             testPushItemID,
				"workspace_id":   testPushWorkspaceID,
				"recipient_type": "member",
				"recipient_id":   testPushUserID,
				"title":          "New inbox item",
				"body":           &body,
				"issue_id":       &issueID,
			},
		},
	}
}

func response(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}
}

func TestDispatchSkipsMutedWorkspace(t *testing.T) {
	store := &fakeStore{
		preference:    db.NotificationPreference{Preferences: []byte(`{"system_notifications":"muted"}`)},
		workspace:     db.Workspace{Slug: "acme"},
		subscriptions: []db.WebPushSubscription{testStoredSubscription()},
	}
	var sends int
	dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
		func(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error) {
			sends++
			return response(http.StatusCreated), nil
		},
	)))

	if err := dispatcher.Dispatch(context.Background(), testInboxEvent()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sends != 0 {
		t.Fatalf("muted workspace sent %d pushes", sends)
	}
}

func TestDispatchBuildsDeepLinkAndSends(t *testing.T) {
	store := &fakeStore{
		preferenceErr: pgx.ErrNoRows,
		workspace:     db.Workspace{Slug: "acme"},
		subscriptions: []db.WebPushSubscription{testStoredSubscription()},
	}
	var payload PushPayload
	dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
		func(_ context.Context, raw []byte, subscription *webpushgo.Subscription, options *webpushgo.Options) (*http.Response, error) {
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if subscription.Endpoint != store.subscriptions[0].Endpoint {
				t.Fatalf("endpoint = %q", subscription.Endpoint)
			}
			if options.VAPIDPrivateKey == "" || options.VAPIDPublicKey == "" {
				t.Fatal("VAPID options were not supplied")
			}
			return response(http.StatusCreated), nil
		},
	)))

	if err := dispatcher.Dispatch(context.Background(), testInboxEvent()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if payload.URL != "/acme/inbox?issue="+testPushIssueID {
		t.Fatalf("url = %q", payload.URL)
	}
	if payload.Tag != testPushItemID || payload.InboxItemID != testPushItemID {
		t.Fatalf("unexpected item identity: %+v", payload)
	}
	if payload.WorkspaceSlug != "acme" || payload.IssueID != testPushIssueID {
		t.Fatalf("unexpected routing payload: %+v", payload)
	}
}

func TestDispatchRetainsSubscriptionOnTransientFailure(t *testing.T) {
	store := &fakeStore{
		preferenceErr: pgx.ErrNoRows,
		workspace:     db.Workspace{Slug: "acme"},
		subscriptions: []db.WebPushSubscription{testStoredSubscription()},
	}
	dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
		func(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error) {
			return response(http.StatusServiceUnavailable), nil
		},
	)))

	if err := dispatcher.Dispatch(context.Background(), testInboxEvent()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(store.deleted) != 0 {
		t.Fatalf("transient failure deleted %d subscriptions", len(store.deleted))
	}
}

func TestDispatchDeletesTerminalSubscription(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := &fakeStore{
				preferenceErr: pgx.ErrNoRows,
				workspace:     db.Workspace{Slug: "acme"},
				subscriptions: []db.WebPushSubscription{testStoredSubscription()},
			}
			dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
				func(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error) {
					return response(status), nil
				},
			)))

			if err := dispatcher.Dispatch(context.Background(), testInboxEvent()); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if len(store.deleted) != 1 || store.deleted[0] != store.subscriptions[0].ID {
				t.Fatalf("deleted = %v, want subscription id", store.deleted)
			}
		})
	}
}

func TestRegisteredListenerDoesNotBlockEventPublication(t *testing.T) {
	release := make(chan struct{})
	store := &fakeStore{
		preferenceErr: pgx.ErrNoRows,
		workspace:     db.Workspace{Slug: "acme"},
		subscriptions: []db.WebPushSubscription{testStoredSubscription()},
		listBlock:     release,
	}
	dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
		func(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error) {
			return response(http.StatusCreated), nil
		},
	)))
	bus := events.New()
	dispatcher.Register(bus)

	returned := make(chan struct{})
	go func() {
		bus.Publish(testInboxEvent())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(100 * time.Millisecond):
		close(release)
		t.Fatal("inbox:new publication blocked on push delivery")
	}
	close(release)
}

func TestRegisteredListenerQueuesNormalBurstWithoutDropping(t *testing.T) {
	const burstSize = 64
	release := make(chan struct{})
	delivered := make(chan struct{}, burstSize)
	store := &fakeStore{
		preferenceErr: pgx.ErrNoRows,
		workspace:     db.Workspace{Slug: "acme"},
		subscriptions: []db.WebPushSubscription{testStoredSubscription()},
		listBlock:     release,
	}
	dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
		func(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error) {
			delivered <- struct{}{}
			return response(http.StatusCreated), nil
		},
	)))
	bus := events.New()
	dispatcher.Register(bus)

	published := make(chan struct{})
	go func() {
		for index := 0; index < burstSize; index++ {
			bus.Publish(testInboxEvent())
		}
		close(published)
	}()
	select {
	case <-published:
	case <-time.After(250 * time.Millisecond):
		close(release)
		t.Fatal("inbox:new burst blocked on push delivery")
	}
	close(release)

	for index := 0; index < burstSize; index++ {
		select {
		case <-delivered:
		case <-time.After(2 * time.Second):
			t.Fatalf("delivered %d of %d queued pushes", index, burstSize)
		}
	}
}

func TestRegisteredListenerBackpressuresAtCapacityWithoutDropping(t *testing.T) {
	const deliveryCount = defaultQueueCapacity + 2
	release := make(chan struct{})
	listStarted := make(chan struct{})
	delivered := make(chan struct{}, deliveryCount)
	store := &fakeStore{
		preferenceErr: pgx.ErrNoRows,
		workspace:     db.Workspace{Slug: "acme"},
		subscriptions: []db.WebPushSubscription{testStoredSubscription()},
		listBlock:     release,
		listStarted:   listStarted,
	}
	dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
		func(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error) {
			delivered <- struct{}{}
			return response(http.StatusCreated), nil
		},
	)))
	dispatcher.workerCount = 1
	bus := events.New()
	dispatcher.Register(bus)

	bus.Publish(testInboxEvent())
	select {
	case <-listStarted:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("worker did not begin the first delivery")
	}
	for index := 0; index < defaultQueueCapacity; index++ {
		bus.Publish(testInboxEvent())
	}

	overflowReturned := make(chan struct{})
	go func() {
		bus.Publish(testInboxEvent())
		close(overflowReturned)
	}()
	select {
	case <-overflowReturned:
		close(release)
		t.Fatal("capacity+1 publish returned before queue capacity was available")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-overflowReturned:
	case <-time.After(time.Second):
		t.Fatal("capacity+1 publish did not resume after queue capacity was available")
	}

	for index := 0; index < deliveryCount; index++ {
		select {
		case <-delivered:
		case <-time.After(2 * time.Second):
			t.Fatalf("delivered %d of %d queued pushes", index, deliveryCount)
		}
	}
}

func TestSendTestReportsMissingSubscription(t *testing.T) {
	store := &fakeStore{}
	dispatcher := NewDispatcher(store, enabledTestConfig(t))
	_, err := dispatcher.SendTest(context.Background(), testPushUserID)
	if !errors.Is(err, ErrNoSubscriptions) {
		t.Fatalf("SendTest error = %v, want ErrNoSubscriptions", err)
	}
}

func TestSendTestMarksPayloadAsTest(t *testing.T) {
	store := &fakeStore{subscriptions: []db.WebPushSubscription{testStoredSubscription()}}
	var payload PushPayload
	dispatcher := NewDispatcher(store, enabledTestConfig(t), WithSender(senderFunc(
		func(_ context.Context, raw []byte, _ *webpushgo.Subscription, _ *webpushgo.Options) (*http.Response, error) {
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			return response(http.StatusCreated), nil
		},
	)))

	result, err := dispatcher.SendTest(context.Background(), testPushUserID)
	if err != nil {
		t.Fatalf("SendTest: %v", err)
	}
	if result.Sent != 1 || !payload.Test {
		t.Fatalf("result=%+v payload=%+v", result, payload)
	}
}
