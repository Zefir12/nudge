package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"git.zefirlabs.net/zefir/nudge/backend/internal/notify"
)

// webhookResponse never includes the URL's path or the header value: either may be a
// secret.
type webhookResponse struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Endpoint   string     `json:"endpoint"`
	HeaderName string     `json:"header_name"`
	Enabled    bool       `json:"enabled"`
	LastSentAt *time.Time `json:"last_sent_at"`
	LastError  string     `json:"last_error"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// webhookRequest creates or updates a webhook; on update a missing or null url or
// header_value keeps the stored one.
type webhookRequest struct {
	Name        string  `json:"name"`
	URL         *string `json:"url"`
	HeaderName  string  `json:"header_name"`
	HeaderValue *string `json:"header_value"`
	Enabled     *bool   `json:"enabled"`
}

// webhookTestResponse is the outcome of a test delivery.
type webhookTestResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func toWebhookResponse(w notify.Webhook) webhookResponse {
	return webhookResponse{
		ID: w.ID, Name: w.Name, Endpoint: w.Endpoint, HeaderName: w.HeaderName, Enabled: w.Enabled,
		LastSentAt: w.LastSentAt, LastError: w.LastError, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
}

func (s *server) listWebhooks(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksAvailable(w) {
		return
	}
	list, err := s.webhooks.List(r.Context())
	if err != nil {
		s.internalError(w, "list webhooks", err)
		return
	}
	resp := make([]webhookResponse, len(list))
	for i, wh := range list {
		resp[i] = toWebhookResponse(wh)
	}
	s.writeJSON(w, http.StatusOK, resp)
}

func (s *server) getWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.webhookID(w, r)
	if !ok {
		return
	}
	wh, err := s.webhooks.Get(r.Context(), id)
	if s.webhookError(w, "get webhook", err) {
		return
	}
	s.writeJSON(w, http.StatusOK, toWebhookResponse(wh))
}

func (s *server) createWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.webhooksAvailable(w) {
		return
	}
	var req webhookRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	wh, err := s.webhooks.Create(r.Context(), notify.Input(req))
	if s.webhookError(w, "create webhook", err) {
		return
	}
	s.log.Info("webhook created", "webhook", wh.ID, "endpoint", wh.Endpoint, "username", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusCreated, toWebhookResponse(wh))
}

func (s *server) updateWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.webhookID(w, r)
	if !ok {
		return
	}
	var req webhookRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	auditTarget(r, req.Name)
	wh, err := s.webhooks.Update(r.Context(), id, notify.Input(req))
	if s.webhookError(w, "update webhook", err) {
		return
	}
	s.log.Info("webhook updated", "webhook", wh.ID, "endpoint", wh.Endpoint, "url_changed", req.URL != nil,
		"header_changed", req.HeaderValue != nil, "username", s.username(r), "ip", clientIP(r))
	s.writeJSON(w, http.StatusOK, toWebhookResponse(wh))
}

func (s *server) deleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.webhookID(w, r)
	if !ok {
		return
	}
	if s.webhookError(w, "delete webhook", s.webhooks.Delete(r.Context(), id)) {
		return
	}
	s.log.Info("webhook deleted", "webhook", id, "username", s.username(r), "ip", clientIP(r))
	w.WriteHeader(http.StatusNoContent)
}

// testWebhook sends a test notification now; a failed delivery is a 200 with its reason,
// as the request itself worked.
func (s *server) testWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.webhookID(w, r)
	if !ok {
		return
	}
	err := s.webhooks.Test(r.Context(), id)
	var derr *notify.DeliveryError
	if errors.As(err, &derr) {
		s.writeJSON(w, http.StatusOK, webhookTestResponse{Error: derr.Msg})
		return
	}
	if s.webhookError(w, "test webhook", err) {
		return
	}
	s.writeJSON(w, http.StatusOK, webhookTestResponse{OK: true})
}

func (s *server) webhooksAvailable(w http.ResponseWriter) bool {
	if s.webhooks == nil {
		s.writeError(w, http.StatusServiceUnavailable, "notifications aren't available")
		return false
	}
	return true
}

func (s *server) webhookID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !s.webhooksAvailable(w) {
		return 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "webhook not found")
		return 0, false
	}
	return id, true
}

// webhookError answers err if there is one and reports whether it did.
func (s *server) webhookError(w http.ResponseWriter, msg string, err error) bool {
	var inputErr *notify.InputError
	switch {
	case err == nil:
		return false
	case errors.As(err, &inputErr):
		s.writeError(w, http.StatusBadRequest, inputErr.Msg)
	case errors.Is(err, notify.ErrNotFound):
		s.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, notify.ErrTaken), errors.Is(err, notify.ErrTooMany):
		s.writeError(w, http.StatusConflict, err.Error())
	default:
		s.internalError(w, msg, err)
	}
	return true
}
