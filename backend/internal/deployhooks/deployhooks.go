// Package deployhooks holds deploy webhooks: a secret URL bound to one container (by
// name) or stack that a CI job or a Forgejo/Gitea/GitHub webhook calls to make it pull
// its image and redeploy. The secret can do nothing else. It is sealed with a
// secretbox.Box rather than hashed, since checking an HMAC signature needs its raw value.
//
// The Service stores the webhooks and their runs, authenticates triggers (a bearer
// secret or an HMAC-SHA256 signature of the body, both compared in constant time) and
// runs the deploys one at a time per webhook (runs.go); the deploy itself is a function
// the caller passes in.
package deployhooks

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
)

const (
	// SecretPrefix starts every webhook secret, so a leaked one is easy to recognise.
	SecretPrefix = "nudge_hook_"
	// MaxPerUser bounds the webhooks one user can create.
	MaxPerUser = 100

	sealPurpose  = "deploy hook"
	secretBytes  = 32
	maxName      = 64
	maxTarget    = 128
	maxTagFilter = 128
)

var (
	// ErrNotFound means there is no such webhook, or it belongs to another user.
	ErrNotFound = errors.New("webhook not found")
	// ErrTaken means the user already has a webhook with that name.
	ErrTaken = errors.New("a webhook with that name already exists")
	// ErrTooMany refuses a webhook over MaxPerUser.
	ErrTooMany = fmt.Errorf("at most %d webhooks per user", MaxPerUser)
	// ErrUnauthorized means the trigger's webhook is unknown, or its credential is
	// missing or wrong; the caller can't tell which.
	ErrUnauthorized = errors.New("authentication required")
)

// InputError is a validation error whose message can be shown to the user.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Kind is what a webhook updates.
type Kind string

const (
	// KindContainer updates a container, found by name.
	KindContainer Kind = "container"
	// KindStack updates a stack: compose up with --pull always.
	KindStack Kind = "stack"
)

// Trigger is how a run's caller authenticated.
type Trigger string

const (
	// TriggerBearer sent the secret in an Authorization header.
	TriggerBearer Trigger = "bearer"
	// TriggerSignature signed the body with the secret (HMAC-SHA256).
	TriggerSignature Trigger = "signature"
)

// Hook is a webhook without its secret.
type Hook struct {
	ID          string
	Name        string
	Environment int64
	Kind        Kind
	// Target is the container or stack name.
	Target string
	// TagFilter is a path.Match pattern a package event's version must match; empty
	// means every event runs.
	TagFilter string
	// CreatedBy is the user the deploys run as; Owner is their username.
	CreatedBy       int64
	Owner           string
	CreatedAt       time.Time
	LastTriggeredAt *time.Time
}

// Input creates a webhook.
type Input struct {
	Name        string
	Environment int64
	Kind        Kind
	Target      string
	TagFilter   string
}

// Actor is the user managing webhooks: users see their own, admins every one.
type Actor struct {
	UserID int64
	Admin  bool
}

// Clock is the time source; tests move it by hand.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options are the Service's dependencies.
type Options struct {
	DB    *sql.DB
	Box   *secretbox.Box
	Log   *slog.Logger
	Clock Clock
	// Context ends the runs (a shutdown); nil means only Close does.
	Context context.Context
}

// Service manages deploy webhooks and runs their deploys. Close stops the runs.
type Service struct {
	db    *sql.DB
	box   *secretbox.Box
	log   *slog.Logger
	clock Clock

	// ctx ends at Close or with Options.Context; runs use it.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool
	// slots holds the webhooks triggered lately: their running and queued run and the
	// trigger rate. Only authenticated webhooks get one, and idle ones are dropped.
	slots map[string]*slot
	// dummy is the key a trigger of an unknown webhook checks against, so it costs
	// what a wrong secret does.
	dummy []byte
}

// New returns a Service.
func New(opts Options) *Service {
	parent := opts.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{
		db: opts.DB, box: opts.Box, log: opts.Log, clock: opts.Clock,
		ctx: ctx, cancel: cancel, slots: map[string]*slot{},
		dummy: []byte(newSecret()),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	return s
}

// newSecret returns a new webhook secret: SecretPrefix and 32 random bytes.
func newSecret() string {
	b := make([]byte, secretBytes)
	_, _ = rand.Read(b) // never fails
	return SecretPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// idPattern matches the IDs newID makes (rand.Text: base32, 26 characters).
var idPattern = regexp.MustCompile(`^[A-Z2-7]{26}$`)

// ValidID reports whether id can be a webhook's.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// containerName matches a Docker container name.
var containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

func (in Input) validate() error {
	if in.Name == "" || len(in.Name) > maxName || !utf8.ValidString(in.Name) || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return &InputError{Msg: fmt.Sprintf("name must be 1 to %d printable bytes", maxName)}
	}
	switch in.Kind {
	case KindContainer, KindStack:
	default:
		return &InputError{Msg: "a webhook updates a container or a stack"}
	}
	if len(in.Target) > maxTarget || !containerName.MatchString(in.Target) {
		return &InputError{Msg: fmt.Sprintf("the %s name is invalid", in.Kind)}
	}
	if len(in.TagFilter) > maxTagFilter || strings.ContainsAny(in.TagFilter, "/ \t\r\n") {
		return &InputError{Msg: fmt.Sprintf("tag_filter must be a tag pattern of at most %d bytes", maxTagFilter)}
	}
	if _, err := path.Match(in.TagFilter, ""); err != nil {
		return &InputError{Msg: "tag_filter is not a valid pattern"}
	}
	return nil
}

// Create stores a webhook made by actor and returns it with its secret, which isn't
// shown again.
func (s *Service) Create(ctx context.Context, actor Actor, in Input) (Hook, string, error) {
	if err := in.validate(); err != nil {
		return Hook{}, "", err
	}
	id, secret := rand.Text(), newSecret()
	now := s.clock.Now().UnixMilli()
	// One statement, so concurrent creates can't pass the limit together.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO deploy_hooks (id, name, environment_id, kind, target, secret_enc, tag_filter, created_by, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT COUNT(*) FROM deploy_hooks WHERE created_by = ?) < ?
		ON CONFLICT (created_by, name) DO NOTHING`,
		id, in.Name, in.Environment, string(in.Kind), in.Target, s.box.Seal([]byte(secret), sealPurpose),
		in.TagFilter, actor.UserID, now, actor.UserID, MaxPerUser)
	if err != nil {
		return Hook{}, "", fmt.Errorf("create webhook: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Hook{}, "", fmt.Errorf("create webhook: %w", err)
	} else if n == 0 {
		var taken bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deploy_hooks WHERE created_by = ? AND name = ?)`,
			actor.UserID, in.Name).Scan(&taken); err != nil {
			return Hook{}, "", fmt.Errorf("create webhook: %w", err)
		}
		if taken {
			return Hook{}, "", ErrTaken
		}
		return Hook{}, "", ErrTooMany
	}
	h, err := s.get(ctx, id)
	if err != nil {
		return Hook{}, "", err
	}
	return h, secret, nil
}

const hookColumns = `h.id, h.name, h.environment_id, h.kind, h.target, h.tag_filter, h.created_by, u.username,
	h.created_at, h.last_triggered_at`

type scanner interface{ Scan(dest ...any) error }

func scanHook(row scanner, extra ...any) (Hook, error) {
	var h Hook
	var created int64
	var last sql.NullInt64
	err := row.Scan(append([]any{&h.ID, &h.Name, &h.Environment, &h.Kind, &h.Target, &h.TagFilter, &h.CreatedBy, &h.Owner,
		&created, &last}, extra...)...)
	if err != nil {
		return Hook{}, err
	}
	h.CreatedAt = time.UnixMilli(created).UTC()
	if last.Valid {
		t := time.UnixMilli(last.Int64).UTC()
		h.LastTriggeredAt = &t
	}
	return h, nil
}

func (s *Service) get(ctx context.Context, id string) (Hook, error) {
	h, err := scanHook(s.db.QueryRowContext(ctx, `SELECT `+hookColumns+`
		FROM deploy_hooks h JOIN users u ON u.id = h.created_by WHERE h.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Hook{}, ErrNotFound
	}
	if err != nil {
		return Hook{}, fmt.Errorf("get webhook: %w", err)
	}
	return h, nil
}

// Get returns webhook id of environment env if actor may see it.
func (s *Service) Get(ctx context.Context, actor Actor, env int64, id string) (Hook, error) {
	if !ValidID(id) {
		return Hook{}, ErrNotFound
	}
	h, err := s.get(ctx, id)
	if err != nil {
		return Hook{}, err
	}
	if h.Environment != env || (!actor.Admin && h.CreatedBy != actor.UserID) {
		return Hook{}, ErrNotFound
	}
	return h, nil
}

// List returns environment env's webhooks actor may see, by name; a kind and target
// narrow it to one container or stack.
func (s *Service) List(ctx context.Context, actor Actor, env int64, kind Kind, target string) ([]Hook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hookColumns+`
		FROM deploy_hooks h JOIN users u ON u.id = h.created_by
		WHERE h.environment_id = ? AND (? OR h.created_by = ?) AND (? = '' OR h.kind = ?) AND (? = '' OR h.target = ?)
		ORDER BY h.name, h.id`, env, actor.Admin, actor.UserID, string(kind), string(kind), target, target)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Hook{}
	for rows.Next() {
		h, err := scanHook(rows)
		if err != nil {
			return nil, fmt.Errorf("list webhooks: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	return out, nil
}

// Delete removes a webhook and its runs. A run under way finishes.
func (s *Service) Delete(ctx context.Context, actor Actor, env int64, id string) error {
	if _, err := s.Get(ctx, actor, env, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM deploy_hooks WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	return nil
}

// Rotate gives a webhook a new secret and returns it; the old one stops working at once.
func (s *Service) Rotate(ctx context.Context, actor Actor, env int64, id string) (Hook, string, error) {
	h, err := s.Get(ctx, actor, env, id)
	if err != nil {
		return Hook{}, "", err
	}
	secret := newSecret()
	res, err := s.db.ExecContext(ctx, `UPDATE deploy_hooks SET secret_enc = ? WHERE id = ?`, s.box.Seal([]byte(secret), sealPurpose), id)
	if err != nil {
		return Hook{}, "", fmt.Errorf("rotate webhook secret: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Hook{}, "", ErrNotFound
	}
	return h, secret, nil
}

// Signature headers, in the order they are checked: Forgejo and Gitea send a hex
// HMAC-SHA256 of the body; GitHub (and Forgejo too) "sha256=" and the hex.
var signatureHeaders = []struct {
	name, prefix string
}{
	{"X-Forgejo-Signature", ""},
	{"X-Gitea-Signature", ""},
	{"X-Hub-Signature-256", "sha256="},
}

// credential is what a trigger authenticates with.
type credential struct {
	trigger Trigger
	bearer  string
	mac     []byte
}

// credentialOf finds the trigger's credential: a webhook secret as the Authorization
// bearer, else the first signature header. Another Authorization header (a proxy's
// Basic auth, an API token) is no credential here and leaves the signature to decide.
func credentialOf(header http.Header) (credential, bool) {
	scheme, token, ok := strings.Cut(header.Get("Authorization"), " ")
	if ok && strings.EqualFold(scheme, "Bearer") && strings.HasPrefix(token, SecretPrefix) {
		return credential{trigger: TriggerBearer, bearer: token}, true
	}
	for _, sh := range signatureHeaders {
		v := header.Get(sh.name)
		if v == "" {
			continue
		}
		hexMAC, ok := strings.CutPrefix(v, sh.prefix)
		if !ok {
			return credential{}, false
		}
		mac, err := hex.DecodeString(hexMAC)
		if err != nil || len(mac) != sha256.Size {
			return credential{}, false
		}
		return credential{trigger: TriggerSignature, mac: mac}, true
	}
	return credential{}, false
}

// verify reports whether c is valid for secret over body, in constant time.
func (c credential) verify(secret, body []byte) bool {
	if c.trigger == TriggerBearer {
		return subtle.ConstantTimeCompare([]byte(c.bearer), secret) == 1
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hmac.Equal(m.Sum(nil), c.mac)
}

// Authenticate checks a trigger of webhook id: its header carries the bearer secret or
// a signature of body. An unknown webhook, a missing and a wrong credential are all
// ErrUnauthorized.
func (s *Service) Authenticate(ctx context.Context, id string, header http.Header, body []byte) (Hook, Trigger, error) {
	c, ok := credentialOf(header)
	if !ok {
		return Hook{}, "", ErrUnauthorized
	}
	var h Hook
	var sealed []byte
	var err error
	if ValidID(id) {
		h, err = scanHook(s.db.QueryRowContext(ctx, `SELECT `+hookColumns+`, h.secret_enc
			FROM deploy_hooks h JOIN users u ON u.id = h.created_by WHERE h.id = ?`, id), &sealed)
	} else {
		err = sql.ErrNoRows
	}
	if errors.Is(err, sql.ErrNoRows) {
		// The same work as a wrong secret, so the answer's time doesn't tell.
		c.verify(s.dummy, body)
		return Hook{}, "", ErrUnauthorized
	}
	if err != nil {
		return Hook{}, "", fmt.Errorf("look up webhook: %w", err)
	}
	secret, err := s.box.Open(sealed, sealPurpose)
	if err != nil {
		return Hook{}, "", fmt.Errorf("open webhook secret: %w", err)
	}
	if !c.verify(secret, body) {
		return Hook{}, "", ErrUnauthorized
	}
	return h, c.trigger, nil
}
