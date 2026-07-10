package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	webpushinternal "github.com/multica-ai/multica/server/internal/webpush"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const webPushRequestBodyLimit = 8 << 10

type WebPushService interface {
	Enabled() bool
	PublicKey() string
	SendTest(ctx context.Context, userID string) (webpushinternal.DeliveryResult, error)
}

type webPushSubscriptionResponse struct {
	ID        string `json:"id"`
	Endpoint  string `json:"endpoint"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (h *Handler) GetWebPushConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	enabled := h.WebPush != nil && h.WebPush.Enabled()
	publicKey := ""
	if enabled {
		publicKey = h.WebPush.PublicKey()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    enabled,
		"public_key": publicKey,
	})
}

func (h *Handler) PutWebPushSubscription(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if h.WebPush == nil || !h.WebPush.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "web push is not configured")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var subscription webpushinternal.Subscription
	if err := decodeWebPushBody(w, r, &subscription); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := webpushinternal.ValidateSubscription(subscription); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stored, err := h.Queries.UpsertWebPushSubscription(r.Context(), db.UpsertWebPushSubscriptionParams{
		UserID:   userUUID,
		Endpoint: subscription.Endpoint,
		P256dh:   subscription.Keys.P256dh,
		Auth:     subscription.Keys.Auth,
	})
	if err != nil {
		slog.Warn("UpsertWebPushSubscription failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to save web push subscription")
		return
	}
	writeJSON(w, http.StatusOK, webPushSubscriptionResponse{
		ID:        util.UUIDToString(stored.ID),
		Endpoint:  stored.Endpoint,
		CreatedAt: util.TimestampToString(stored.CreatedAt),
		UpdatedAt: util.TimestampToString(stored.UpdatedAt),
	})
}

func (h *Handler) DeleteWebPushSubscription(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var request struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decodeWebPushBody(w, r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := webpushinternal.ValidateEndpoint(request.Endpoint); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := h.Queries.DeleteWebPushSubscriptionByEndpoint(r.Context(), db.DeleteWebPushSubscriptionByEndpointParams{
		UserID:   userUUID,
		Endpoint: request.Endpoint,
	}); err != nil {
		slog.Warn("DeleteWebPushSubscriptionByEndpoint failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to delete web push subscription")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) SendWebPushTest(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if h.WebPush == nil || !h.WebPush.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "web push is not configured")
		return
	}
	result, err := h.WebPush.SendTest(r.Context(), userID)
	if err != nil {
		switch {
		case errors.Is(err, webpushinternal.ErrNoSubscriptions):
			writeError(w, http.StatusConflict, "no web push subscription is registered")
		case errors.Is(err, webpushinternal.ErrDeliveryFailed):
			writeError(w, http.StatusBadGateway, "web push test delivery failed")
		default:
			slog.Warn("web push test failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to send web push test")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func decodeWebPushBody(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, webPushRequestBodyLimit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}
