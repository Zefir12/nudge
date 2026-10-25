package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Purposes bind the sealed secrets to this use of the key.
const (
	urlPurpose    = "webhook url"
	headerPurpose = "webhook header"
)

const (
	maxName        = 64
	maxURL         = 2048
	maxHeaderValue = 4096
	// MaxWebhooks bounds the targets, and so the deliveries of one notification.
	MaxWebhooks = 20
)

// Events are the events a webhook can receive, in the order they are stored.
var Events = []string{EventUpdateAvailable, EventUnhealthy, EventHealthy, EventExited, EventAgentOffline, EventAgentOnline}

// DefaultEvents are a new webhook's events when its Input names none: the problems,
// without the recoveries.
var DefaultEvents = []string{EventUpdateAvailable, EventUnhealthy, EventExited, EventAgentOffline}

var (
	// ErrNotFound means there is no webhook with that id.
	ErrNotFound = errors.New("webhook not found")
	// ErrTaken means another webhook has that name.
	ErrTaken = errors.New("a webhook with that name already exists")
	// ErrTooMany refuses a webhook over MaxWebhooks.
	ErrTooMany = fmt.Errorf("at most %d webhooks", MaxWebhooks)
)

// InputError is a validation error whose message can be shown to the user.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Webhook is a target as the API shows it: without the URL's path and the header value.
type Webhook struct {
	ID   int64
	Name string
	// Endpoint is the URL's scheme and host.
	Endpoint   string
	HeaderName string
	Enabled    bool
	// Events are the events it receives, in the order of Events.
	Events []string
	// LastSentAt is the last delivery, nil if there was none; LastError says why it
	// failed, empty if it didn't.
	LastSentAt *time.Time
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Input creates or updates a webhook. On update a nil URL keeps the stored one, a nil
// HeaderValue the stored value (unless the endpoint or header name changes, so a secret
// never follows the URL to another host) and a nil Enabled the stored state; an empty
// HeaderName sends no header. A new webhook is disabled unless Enabled says otherwise.
// Nil Events keep the stored ones, or are DefaultEvents for a new webhook.
type Input struct {
	Name        string
	URL         *string
	HeaderName  string
	HeaderValue *string
	Enabled     *bool
	Events      []string
}

// target is a webhook with its secrets, for a delivery.
type target struct {
	id          int64
	name        string
	url         string
	headerName  string
	headerValue string
}

const selectWebhook = `SELECT id, name, endpoint, header_name, enabled, events, last_sent_at, last_error, created_at, updated_at FROM webhooks`

// List returns every webhook by name.
func (s *Service) List(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, selectWebhook+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var list []Webhook
	for rows.Next() {
		w, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list webhooks: %w", err)
		}
		list = append(list, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	return list, nil
}

// Get returns the webhook with id.
func (s *Service) Get(ctx context.Context, id int64) (Webhook, error) {
	w, err := scan(s.db.QueryRowContext(ctx, selectWebhook+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Webhook{}, ErrNotFound
	}
	if err != nil {
		return Webhook{}, fmt.Errorf("get webhook: %w", err)
	}
	return w, nil
}

// Create stores a webhook; the URL is required, and the header value with a header name.
func (s *Service) Create(ctx context.Context, in Input) (Webhook, error) {
	if in.URL == nil {
		return Webhook{}, &InputError{Msg: "url is required"}
	}
	if in.HeaderName != "" && in.HeaderValue == nil {
		return Webhook{}, &InputError{Msg: "header value is required with a header name"}
	}
	if in.Events == nil {
		in.Events = DefaultEvents
	}
	endpoint, err := validate(&in)
	if err != nil {
		return Webhook{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Webhook{}, fmt.Errorf("create webhook: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM webhooks`).Scan(&n); err != nil {
		return Webhook{}, fmt.Errorf("create webhook: %w", err)
	}
	if n >= MaxWebhooks {
		return Webhook{}, ErrTooMany
	}
	now := s.clock.Now().Unix()
	res, err := tx.ExecContext(ctx, `INSERT INTO webhooks (name, endpoint, url_enc, header_name, header_value_enc, enabled, events, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		in.Name, endpoint, s.box.Seal([]byte(*in.URL), urlPurpose), in.HeaderName, s.sealHeader(in), in.Enabled != nil && *in.Enabled,
		strings.Join(in.Events, ","), now, now)
	if err != nil {
		return Webhook{}, fmt.Errorf("create webhook: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return Webhook{}, fmt.Errorf("create webhook: %w", err)
		}
		return Webhook{}, ErrTaken
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Webhook{}, fmt.Errorf("create webhook: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Webhook{}, fmt.Errorf("create webhook: %w", err)
	}
	return s.Get(ctx, id)
}

// Update replaces a webhook's fields, keeping the URL and header value that in leaves nil.
func (s *Service) Update(ctx context.Context, id int64, in Input) (Webhook, error) {
	endpoint, err := validate(&in)
	if err != nil {
		return Webhook{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Webhook{}, fmt.Errorf("update webhook: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var oldEndpoint, oldHeader, events string
	var urlEnc, headerEnc []byte
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT endpoint, url_enc, header_name, header_value_enc, enabled, events FROM webhooks WHERE id = ?`, id).
		Scan(&oldEndpoint, &urlEnc, &oldHeader, &headerEnc, &enabled, &events)
	if errors.Is(err, sql.ErrNoRows) {
		return Webhook{}, ErrNotFound
	}
	if err != nil {
		return Webhook{}, fmt.Errorf("update webhook: %w", err)
	}
	if in.URL != nil {
		urlEnc = s.box.Seal([]byte(*in.URL), urlPurpose)
	} else {
		endpoint = oldEndpoint
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.Events != nil {
		events = strings.Join(in.Events, ",")
	}
	switch {
	case in.HeaderName == "":
		headerEnc = nil
	case in.HeaderValue != nil:
		headerEnc = s.sealHeader(in)
	case headerEnc == nil:
		return Webhook{}, &InputError{Msg: "header value is required with a header name"}
	case endpoint != oldEndpoint || in.HeaderName != oldHeader:
		return Webhook{}, &InputError{Msg: "header value is required when the url's host or the header name changes"}
	}
	var taken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM webhooks WHERE id != ? AND name = ?)`, id, in.Name).Scan(&taken); err != nil {
		return Webhook{}, fmt.Errorf("update webhook: %w", err)
	}
	if taken {
		return Webhook{}, ErrTaken
	}
	if _, err := tx.ExecContext(ctx, `UPDATE webhooks SET name = ?, endpoint = ?, url_enc = ?, header_name = ?, header_value_enc = ?, enabled = ?, events = ?, updated_at = ?
		WHERE id = ?`, in.Name, endpoint, urlEnc, in.HeaderName, headerEnc, enabled, events, s.clock.Now().Unix(), id); err != nil {
		return Webhook{}, fmt.Errorf("update webhook: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Webhook{}, fmt.Errorf("update webhook: %w", err)
	}
	return s.Get(ctx, id)
}

// Delete removes the webhook with id.
func (s *Service) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) sealHeader(in Input) []byte {
	if in.HeaderName == "" {
		return nil
	}
	return s.box.Seal([]byte(*in.HeaderValue), headerPurpose)
}

// targets returns the enabled webhooks that receive event with their secrets, or the one
// with id if id > 0 (enabled or not, whatever its events: a test may check one before
// turning it on).
func (s *Service) targets(ctx context.Context, id int64, event string) ([]target, error) {
	query := `SELECT id, name, url_enc, header_name, header_value_enc, events FROM webhooks WHERE enabled`
	args := []any{}
	if id > 0 {
		query, args = `SELECT id, name, url_enc, header_name, header_value_enc, events FROM webhooks WHERE id = ?`, []any{id}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("webhook targets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var list []target
	for rows.Next() {
		var t target
		var urlEnc, headerEnc []byte
		var events string
		if err := rows.Scan(&t.id, &t.name, &urlEnc, &t.headerName, &headerEnc, &events); err != nil {
			return nil, fmt.Errorf("webhook targets: %w", err)
		}
		if id == 0 && !slices.Contains(strings.Split(events, ","), event) {
			continue
		}
		u, err := s.box.Open(urlEnc, urlPurpose)
		if err != nil {
			return nil, fmt.Errorf("webhook %d url: %w", t.id, err)
		}
		t.url = string(u)
		if t.headerName != "" {
			v, err := s.box.Open(headerEnc, headerPurpose)
			if err != nil {
				return nil, fmt.Errorf("webhook %d header: %w", t.id, err)
			}
			t.headerValue = string(v)
		}
		list = append(list, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("webhook targets: %w", err)
	}
	return list, nil
}

// recordDelivery notes the outcome of a delivery to webhook id; best effort.
func (s *Service) recordDelivery(ctx context.Context, id int64, msg string) {
	if _, err := s.db.ExecContext(ctx, `UPDATE webhooks SET last_sent_at = ?, last_error = ? WHERE id = ?`,
		s.clock.Now().Unix(), msg, id); err != nil {
		s.log.Warn("record webhook delivery", "webhook", id, "err", err)
	}
}

// headerToken is an HTTP header name.
var headerToken = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]{1,64}$")

// reservedHeaders are set by nudge or the transport.
var reservedHeaders = map[string]bool{
	"Host": true, "Content-Type": true, "Content-Length": true, "Transfer-Encoding": true,
	"Connection": true, "User-Agent": true, "X-Nudge-Event": true,
}

// validate checks in and returns the URL's endpoint, empty when in.URL is nil.
func validate(in *Input) (string, error) {
	if in.Name == "" || len(in.Name) > maxName {
		return "", &InputError{Msg: fmt.Sprintf("name must be 1 to %d bytes", maxName)}
	}
	if !utf8.ValidString(in.Name) || strings.TrimSpace(in.Name) != in.Name || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return "", &InputError{Msg: "name must be valid UTF-8 without control characters or surrounding spaces"}
	}
	if in.HeaderName != "" {
		if !headerToken.MatchString(in.HeaderName) {
			return "", &InputError{Msg: "header name must be an HTTP header name"}
		}
		in.HeaderName = http.CanonicalHeaderKey(in.HeaderName)
		if reservedHeaders[in.HeaderName] {
			return "", &InputError{Msg: in.HeaderName + " is set by nudge"}
		}
	}
	if v := in.HeaderValue; v != nil && in.HeaderName != "" {
		if *v == "" || len(*v) > maxHeaderValue || strings.IndexFunc(*v, unicode.IsControl) >= 0 || !utf8.ValidString(*v) {
			return "", &InputError{Msg: fmt.Sprintf("header value must be 1 to %d bytes without control characters", maxHeaderValue)}
		}
	}
	if in.Events != nil {
		events, err := validEvents(in.Events)
		if err != nil {
			return "", err
		}
		in.Events = events
	}
	if in.URL == nil {
		return "", nil
	}
	return endpointOf(*in.URL)
}

// endpointOf checks a webhook URL and returns its scheme and host.
func endpointOf(raw string) (string, error) {
	const msg = "url must be an http or https URL without credentials"
	if raw == "" || len(raw) > maxURL || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", &InputError{Msg: fmt.Sprintf("url must be 1 to %d bytes without control characters", maxURL)}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", &InputError{Msg: msg}
	}
	return u.Scheme + "://" + u.Host, nil
}

// validEvents checks a webhook's events and returns them in the order of Events.
func validEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return nil, &InputError{Msg: "events must name at least one event"}
	}
	for _, e := range events {
		if !slices.Contains(Events, e) {
			return nil, &InputError{Msg: fmt.Sprintf("unknown event %q", e)}
		}
	}
	var list []string
	for _, e := range Events {
		if slices.Contains(events, e) {
			list = append(list, e)
		}
	}
	return list, nil
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Webhook, error) {
	var w Webhook
	var lastSent sql.NullInt64
	var created, updated int64
	var events string
	if err := row.Scan(&w.ID, &w.Name, &w.Endpoint, &w.HeaderName, &w.Enabled, &events, &lastSent, &w.LastError, &created, &updated); err != nil {
		return Webhook{}, err
	}
	w.Events = strings.Split(events, ",")
	if lastSent.Valid {
		t := time.Unix(lastSent.Int64, 0).UTC()
		w.LastSentAt = &t
	}
	w.CreatedAt = time.Unix(created, 0).UTC()
	w.UpdatedAt = time.Unix(updated, 0).UTC()
	return w, nil
}
