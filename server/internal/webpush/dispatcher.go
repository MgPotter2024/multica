package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	defaultDeliveryTimeout = 5 * time.Second
	defaultDispatchTimeout = 12 * time.Second
	defaultWorkerCount     = 8
	defaultQueueCapacity   = 256
	defaultTTLSeconds      = 300
	maxTitleRunes          = 180
	maxBodyRunes           = 700
)

var (
	ErrNoSubscriptions = errors.New("no web push subscriptions")
	ErrDeliveryFailed  = errors.New("web push delivery failed")
)

type Store interface {
	GetNotificationPreference(context.Context, db.GetNotificationPreferenceParams) (db.NotificationPreference, error)
	GetWorkspace(context.Context, pgtype.UUID) (db.Workspace, error)
	ListWebPushSubscriptionsByUser(context.Context, pgtype.UUID) ([]db.WebPushSubscription, error)
	DeleteWebPushSubscriptionByID(context.Context, pgtype.UUID) error
}

type Sender interface {
	Send(context.Context, []byte, *webpushgo.Subscription, *webpushgo.Options) (*http.Response, error)
}

type defaultSender struct{}

func (defaultSender) Send(ctx context.Context, payload []byte, subscription *webpushgo.Subscription, options *webpushgo.Options) (*http.Response, error) {
	return webpushgo.SendNotificationWithContext(ctx, payload, subscription, options)
}

type Option func(*Dispatcher)

func WithSender(sender Sender) Option {
	return func(dispatcher *Dispatcher) {
		if sender != nil {
			dispatcher.sender = sender
		}
	}
}

type Dispatcher struct {
	store           Store
	config          Config
	sender          Sender
	httpClient      *http.Client
	deliveryTimeout time.Duration
	dispatchTimeout time.Duration
	queue           chan events.Event
	workerCount     int
	startOnce       sync.Once
}

type PushPayload struct {
	Title         string `json:"title"`
	Body          string `json:"body"`
	URL           string `json:"url"`
	Tag           string `json:"tag"`
	InboxItemID   string `json:"inbox_item_id"`
	IssueID       string `json:"issue_id,omitempty"`
	WorkspaceSlug string `json:"workspace_slug"`
	Test          bool   `json:"test,omitempty"`
}

type DeliveryResult struct {
	Sent    int `json:"sent"`
	Failed  int `json:"failed"`
	Removed int `json:"removed"`
}

func NewDispatcher(store Store, config Config, options ...Option) *Dispatcher {
	dispatcher := &Dispatcher{
		store:           store,
		config:          config,
		sender:          defaultSender{},
		deliveryTimeout: defaultDeliveryTimeout,
		dispatchTimeout: defaultDispatchTimeout,
		queue:           make(chan events.Event, defaultQueueCapacity),
		workerCount:     defaultWorkerCount,
	}
	dispatcher.httpClient = newRestrictedHTTPClient(dispatcher.deliveryTimeout)
	for _, option := range options {
		option(dispatcher)
	}
	return dispatcher
}

func (d *Dispatcher) Enabled() bool {
	return d != nil && d.config.Enabled()
}

func (d *Dispatcher) PublicKey() string {
	if !d.Enabled() {
		return ""
	}
	return d.config.PublicKey()
}

func (d *Dispatcher) Register(bus *events.Bus) {
	if !d.Enabled() || bus == nil {
		return
	}
	d.startWorkers()
	bus.Subscribe(protocol.EventInboxNew, d.DispatchAsync)
}

func (d *Dispatcher) DispatchAsync(event events.Event) {
	if !d.Enabled() {
		return
	}
	d.startWorkers()
	d.queue <- event
}

func (d *Dispatcher) startWorkers() {
	d.startOnce.Do(func() {
		for worker := 0; worker < d.workerCount; worker++ {
			go d.runWorker()
		}
	})
}

func (d *Dispatcher) runWorker() {
	for event := range d.queue {
		d.dispatchQueuedEvent(event)
	}
}

func (d *Dispatcher) dispatchQueuedEvent(event events.Event) {
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("web push dispatcher recovered panic",
				"event_type", event.Type,
				"recovered", recovered,
				"stack", string(debug.Stack()),
			)
		}
	}()
	if err := d.Dispatch(context.Background(), event); err != nil {
		slog.Warn("web push dispatch failed", "event_type", event.Type, "error", err)
	}
}

func (d *Dispatcher) Dispatch(ctx context.Context, event events.Event) error {
	if !d.Enabled() {
		return nil
	}
	item, ok := parseInboxEvent(event)
	if !ok {
		return nil
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, d.dispatchTimeout)
	defer cancel()

	muted, err := d.isMuted(dispatchCtx, item.workspaceID, item.recipientID)
	if err != nil {
		return err
	}
	if muted {
		return nil
	}

	workspace, err := d.store.GetWorkspace(dispatchCtx, item.workspaceID)
	if err != nil {
		return fmt.Errorf("load push workspace: %w", err)
	}
	if strings.TrimSpace(workspace.Slug) == "" {
		return errors.New("load push workspace: empty slug")
	}
	subscriptions, err := d.store.ListWebPushSubscriptionsByUser(dispatchCtx, item.recipientID)
	if err != nil {
		return fmt.Errorf("list web push subscriptions: %w", err)
	}
	if len(subscriptions) == 0 {
		return nil
	}

	issueKey := item.issueID
	if issueKey == "" {
		issueKey = item.id
	}
	payload := PushPayload{
		Title:         truncateRunes(item.title, maxTitleRunes),
		Body:          truncateRunes(item.body, maxBodyRunes),
		URL:           "/" + url.PathEscape(workspace.Slug) + "/inbox?issue=" + url.QueryEscape(issueKey),
		Tag:           item.id,
		InboxItemID:   item.id,
		IssueID:       item.issueID,
		WorkspaceSlug: workspace.Slug,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode web push payload: %w", err)
	}
	d.deliver(dispatchCtx, encoded, subscriptions)
	return nil
}

func (d *Dispatcher) SendTest(ctx context.Context, userID string) (DeliveryResult, error) {
	if !d.Enabled() {
		return DeliveryResult{}, errors.New("web push is disabled")
	}
	userUUID, err := util.ParseUUID(userID)
	if err != nil {
		return DeliveryResult{}, errors.New("invalid user id")
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, d.dispatchTimeout)
	defer cancel()
	subscriptions, err := d.store.ListWebPushSubscriptionsByUser(dispatchCtx, userUUID)
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("list web push subscriptions: %w", err)
	}
	if len(subscriptions) == 0 {
		return DeliveryResult{}, ErrNoSubscriptions
	}
	payload, err := json.Marshal(PushPayload{
		Title: "Multica notifications are working",
		Body:  "You will receive notifications for new inbox items.",
		URL:   "/",
		Tag:   "multica-web-push-test",
		Test:  true,
	})
	if err != nil {
		return DeliveryResult{}, fmt.Errorf("encode web push test payload: %w", err)
	}
	result := d.deliver(dispatchCtx, payload, subscriptions)
	if result.Sent == 0 {
		return result, ErrDeliveryFailed
	}
	return result, nil
}

func (d *Dispatcher) deliver(ctx context.Context, payload []byte, subscriptions []db.WebPushSubscription) DeliveryResult {
	result := DeliveryResult{}
	for _, stored := range subscriptions {
		if ctx.Err() != nil {
			result.Failed += len(subscriptions) - result.Sent - result.Failed - result.Removed
			break
		}
		subscription := &webpushgo.Subscription{
			Endpoint: stored.Endpoint,
			Keys: webpushgo.Keys{
				P256dh: stored.P256dh,
				Auth:   stored.Auth,
			},
		}
		sendCtx, cancel := context.WithTimeout(ctx, d.deliveryTimeout)
		response, err := d.sender.Send(sendCtx, payload, subscription, &webpushgo.Options{
			HTTPClient:      d.httpClient,
			Subscriber:      d.config.subject,
			VAPIDPublicKey:  d.config.publicKey,
			VAPIDPrivateKey: d.config.privateKey,
			TTL:             defaultTTLSeconds,
			Urgency:         webpushgo.UrgencyNormal,
		})
		cancel()
		if err != nil {
			slog.Warn("web push delivery request failed",
				"subscription_id", util.UUIDToString(stored.ID),
				"error", err,
			)
			result.Failed++
			continue
		}
		if response == nil {
			slog.Warn("web push delivery returned no response",
				"subscription_id", util.UUIDToString(stored.ID),
			)
			result.Failed++
			continue
		}
		if response.Body != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
		}
		switch {
		case response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices:
			result.Sent++
		case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
			if err := d.store.DeleteWebPushSubscriptionByID(ctx, stored.ID); err != nil {
				slog.Warn("web push stale subscription cleanup failed",
					"subscription_id", util.UUIDToString(stored.ID),
					"status", response.StatusCode,
					"error", err,
				)
				result.Failed++
				continue
			}
			result.Removed++
		default:
			slog.Warn("web push delivery rejected",
				"subscription_id", util.UUIDToString(stored.ID),
				"status", response.StatusCode,
			)
			result.Failed++
		}
	}
	return result
}

func (d *Dispatcher) isMuted(ctx context.Context, workspaceID, userID pgtype.UUID) (bool, error) {
	preference, err := d.store.GetNotificationPreference(ctx, db.GetNotificationPreferenceParams{
		WorkspaceID: workspaceID,
		UserID:      userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load notification preference: %w", err)
	}
	var preferences map[string]string
	if len(preference.Preferences) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(preference.Preferences, &preferences); err != nil {
		return false, nil
	}
	return preferences["system_notifications"] == "muted", nil
}

type inboxEventItem struct {
	id          string
	workspaceID pgtype.UUID
	recipientID pgtype.UUID
	issueID     string
	title       string
	body        string
}

func parseInboxEvent(event events.Event) (inboxEventItem, bool) {
	if event.Type != protocol.EventInboxNew || event.WorkspaceID == "" {
		return inboxEventItem{}, false
	}
	payload, ok := event.Payload.(map[string]any)
	if !ok {
		return inboxEventItem{}, false
	}
	item, ok := payload["item"].(map[string]any)
	if !ok || stringValue(item["recipient_type"]) != "member" {
		return inboxEventItem{}, false
	}
	workspaceID, err := util.ParseUUID(event.WorkspaceID)
	if err != nil {
		return inboxEventItem{}, false
	}
	if payloadWorkspaceID := stringValue(item["workspace_id"]); payloadWorkspaceID != "" && payloadWorkspaceID != event.WorkspaceID {
		return inboxEventItem{}, false
	}
	recipientID, err := util.ParseUUID(stringValue(item["recipient_id"]))
	if err != nil {
		return inboxEventItem{}, false
	}
	id := stringValue(item["id"])
	if _, err := util.ParseUUID(id); err != nil {
		return inboxEventItem{}, false
	}
	issueID := stringValue(item["issue_id"])
	if issueID != "" {
		if _, err := util.ParseUUID(issueID); err != nil {
			return inboxEventItem{}, false
		}
	}
	title := stringValue(item["title"])
	if title == "" {
		title = "Multica"
	}
	return inboxEventItem{
		id:          id,
		workspaceID: workspaceID,
		recipientID: recipientID,
		issueID:     issueID,
		title:       title,
		body:        stringValue(item["body"]),
	}, true
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case *string:
		if typed != nil {
			return *typed
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
