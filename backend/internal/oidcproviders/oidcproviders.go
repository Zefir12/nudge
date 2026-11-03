// Package oidcproviders holds the single sign-on providers: the read-only one from the
// environment (NUDGE_OIDC_*) and the ones an admin manages, stored with the client secret
// sealed (it must be sent to the provider, so it can't be hashed; it never leaves the
// package except to the provider). It keeps one oidc.Client per enabled provider.
//
// Taking a provider away (disabling or deleting it, or moving it to another issuer) ends
// its users' sessions and deletes their API tokens in the same transaction, since nobody
// can verify those users any more. Their accounts stay, so bringing the issuer back signs
// them in again with what they own. A change that would leave no admin able to sign in is
// refused.
package oidcproviders

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"git.zefirlabs.net/zefir/nudge/backend/internal/auth"
	"git.zefirlabs.net/zefir/nudge/backend/internal/oidc"
	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
)

const (
	// EnvID is the id of the provider from the environment, reserved for it.
	EnvID = "env"
	// sealPurpose binds sealed client secrets to this use of the key.
	sealPurpose = "oidc provider"

	// CallbackBase is the path every provider's login and callback sit under.
	CallbackBase = "/api/v1/auth/oidc"

	// MaxProviders bounds the login buttons and the clients kept in memory.
	MaxProviders = 32
	maxID        = 40
	maxName      = 64
	maxIssuer    = 2048
	maxClientID  = 255
	maxSecret    = 4096
	maxRole      = 255

	// DefaultSessionTTL is a provider's session length when the input gives none.
	DefaultSessionTTL = 12 * time.Hour
	// MinSessionTTL is the shortest session length.
	MinSessionTTL = 5 * time.Minute
	// MaxSessionTTL is the longest: the provider can't end a nudge session, so its length
	// is how long a user removed there keeps access.
	MaxSessionTTL = 30 * 24 * time.Hour
)

var (
	// ErrNotFound means there is no provider with that id.
	ErrNotFound = errors.New("provider not found")
	// ErrTaken means another provider has that id, name or issuer.
	ErrTaken = errors.New("a provider with that id, name or issuer already exists")
	// ErrReadOnly means the provider comes from the environment.
	ErrReadOnly = errors.New("this provider comes from the NUDGE_OIDC_* settings and can't be changed here")
	// ErrNoAdminLeft means the change would leave no enabled admin able to sign in.
	ErrNoAdminLeft = errors.New("no enabled admin could sign in after this: give an admin a password or a sign-in through another enabled provider first")
	// ErrLastProvider means password login is off and this is the last enabled provider.
	ErrLastProvider = errors.New("password login is off (NUDGE_PASSWORD_LOGIN=false), so at least one provider must stay enabled")
	// ErrGone means the provider of a login in progress changed or went away meanwhile.
	ErrGone = errors.New("sso provider changed during the login")
)

// InputError is a validation error whose message can be shown to the user.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Provider is a provider's settings, without the client secret.
type Provider struct {
	ID         string
	Name       string
	Issuer     string
	ClientID   string
	AdminRole  string
	UserRole   string
	RequireMFA bool
	SessionTTL time.Duration
	Enabled    bool
	// ReadOnly says it comes from the environment.
	ReadOnly bool
	// SecretUnreadable says the stored secret can't be decrypted (secret.key changed):
	// the provider can't be used until the secret is entered again.
	SecretUnreadable bool
	// CallbackURL is the redirect URI to register at the provider; empty without an https
	// public URL.
	CallbackURL string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Input creates or replaces a provider. ID is only read on create (empty: derived from
// Name). A nil ClientSecret keeps the stored one on update. A nil Enabled is true on
// create and unchanged on update. An empty UserRole signs in admins only; a zero
// SessionTTL is DefaultSessionTTL.
type Input struct {
	ID           string
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret *string
	AdminRole    string
	UserRole     string
	RequireMFA   bool
	SessionTTL   time.Duration
	Enabled      *bool
}

// Env is the provider set by NUDGE_OIDC_*. Its secret stays in the environment.
type Env struct {
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret string
	AdminRole    string
	UserRole     string
	RequireMFA   bool
	SessionTTL   time.Duration
}

// Options are the Service's dependencies.
type Options struct {
	DB   *sql.DB
	Box  *secretbox.Box
	Auth *auth.Service
	// Env is the provider from the environment, nil if none.
	Env *Env
	// PublicURL is what the redirect URIs start with; single sign-on needs it to be https.
	PublicURL string
	// PasswordLoginOff keeps at least one provider enabled.
	PasswordLoginOff bool
	// HTTPClient calls the providers (nil: the oidc package's default).
	HTTPClient *http.Client
	// Now is the clock (time.Now by default).
	Now func() time.Time
	Log *slog.Logger
}

// Login is an enabled provider with its client.
type Login struct {
	Provider Provider
	Client   *oidc.Client
}

// Service manages the providers.
type Service struct {
	db         *sql.DB
	box        *secretbox.Box
	auth       *auth.Service
	publicURL  string
	pwOff      bool
	httpClient *http.Client
	now        func() time.Time
	log        *slog.Logger

	// mu is held for writing across a change (its transaction and the swap of the client)
	// and for reading while Complete finishes a login, so a login verified against a
	// provider can't start a session after that provider's users were revoked. It is
	// never held across a call to a provider.
	mu  sync.RWMutex
	env *Login
	// logins are the enabled, usable providers by id, the environment's included.
	logins map[string]*Login
}

// New returns a Service holding the environment's provider; Load adds the stored ones.
func New(opts Options) *Service {
	s := &Service{
		db: opts.DB, box: opts.Box, auth: opts.Auth, publicURL: opts.PublicURL, pwOff: opts.PasswordLoginOff,
		httpClient: opts.HTTPClient, now: opts.Now, log: opts.Log, logins: map[string]*Login{},
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if e := opts.Env; e != nil {
		p := Provider{
			ID: EnvID, Name: e.Name, Issuer: e.Issuer, ClientID: e.ClientID, AdminRole: e.AdminRole, UserRole: e.UserRole,
			RequireMFA: e.RequireMFA, SessionTTL: e.SessionTTL, Enabled: true, ReadOnly: true,
			// The URI registered before providers could be managed keeps working.
			CallbackURL: s.publicURL + CallbackBase + "/callback",
		}
		s.env = &Login{Provider: p, Client: s.client(p, e.ClientSecret)}
		s.logins[EnvID] = s.env
	}
	return s
}

func (s *Service) client(p Provider, secret string) *oidc.Client {
	return oidc.New(oidc.Options{
		Issuer: p.Issuer, ClientID: p.ClientID, ClientSecret: secret, RedirectURL: p.CallbackURL,
		AdminRole: p.AdminRole, UserRole: p.UserRole, RequireMFA: p.RequireMFA,
		HTTPClient: s.httpClient, Now: s.now, Log: s.log,
	})
}

// httpsPublicURL reports whether redirect URIs can be built; a browser's binding cookie
// and the providers both need https.
func (s *Service) httpsPublicURL() bool { return strings.HasPrefix(s.publicURL, "https://") }

// CallbackPrefix is what a new provider's callback URL starts with (its id and /callback
// follow), empty without an https public URL.
func (s *Service) CallbackPrefix() string {
	if !s.httpsPublicURL() {
		return ""
	}
	return s.publicURL + CallbackBase + "/"
}

// row is a stored provider.
type row struct {
	Provider
	sealed []byte
}

const columns = `id, name, issuer, client_id, client_secret, admin_role, user_role, require_mfa, session_ttl_seconds, enabled, created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func (s *Service) scan(sc scanner) (row, error) {
	var r row
	var ttl, created, updated int64
	if err := sc.Scan(&r.ID, &r.Name, &r.Issuer, &r.ClientID, &r.sealed, &r.AdminRole, &r.UserRole, &r.RequireMFA, &ttl, &r.Enabled, &created, &updated); err != nil {
		return row{}, err
	}
	r.SessionTTL = time.Duration(ttl) * time.Second
	r.CreatedAt, r.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	if prefix := s.CallbackPrefix(); prefix != "" {
		r.CallbackURL = prefix + r.ID + "/callback"
	}
	_, err := s.open(r)
	r.SecretUnreadable = err != nil
	return r, nil
}

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Service) rows(ctx context.Context, q querier) ([]row, error) {
	res, err := q.QueryContext(ctx, `SELECT `+columns+` FROM oidc_providers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer func() { _ = res.Close() }()
	var out []row
	for res.Next() {
		r, err := s.scan(res)
		if err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		out = append(out, r)
	}
	if err := res.Err(); err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	return out, nil
}

// purpose binds a sealed secret to its provider and issuer, so a database edit can't send
// it to another server.
func purpose(id, issuer string) string { return sealPurpose + "\x00" + id + "\x00" + issuer }

func (s *Service) open(r row) (string, error) {
	b, err := s.box.Open(r.sealed, purpose(r.ID, r.Issuer))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Load reads the stored providers and readies a client for each enabled one. A provider
// that can't be used (unreadable secret, no https public URL) is logged and left out, so
// it never stops nudge from starting.
func (s *Service) Load(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.rows(ctx, s.db)
	if err != nil {
		return err
	}
	for _, r := range rows {
		s.install(r)
	}
	return nil
}

// install puts r's client in place of any older one; s.mu is held for writing.
func (s *Service) install(r row) {
	delete(s.logins, r.ID)
	if !r.Enabled {
		return
	}
	if !s.httpsPublicURL() {
		s.log.Warn("single sign-on provider left out: it needs an https:// public URL (NUDGE_PUBLIC_URL)", "provider", r.ID)
		return
	}
	secret, err := s.open(r)
	if err != nil {
		s.log.Error("single sign-on provider left out: enter its client secret again", "provider", r.ID, "err", err)
		return
	}
	s.logins[r.ID] = &Login{Provider: r.Provider, Client: s.client(r.Provider, secret)}
}

// List returns the environment's provider, if any, then the stored ones by name.
func (s *Service) List(ctx context.Context) ([]Provider, error) {
	rows, err := s.rows(ctx, s.db)
	if err != nil {
		return nil, err
	}
	out := make([]Provider, 0, len(rows)+1)
	if s.env != nil {
		out = append(out, s.env.Provider)
	}
	for _, r := range rows {
		out = append(out, r.Provider)
	}
	return out, nil
}

// Get returns one provider.
func (s *Service) Get(ctx context.Context, id string) (Provider, error) {
	if id == EnvID && s.env != nil {
		return s.env.Provider, nil
	}
	r, err := s.scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM oidc_providers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Provider{}, ErrNotFound
	}
	if err != nil {
		return Provider{}, fmt.Errorf("get provider: %w", err)
	}
	return r.Provider, nil
}

// Enabled returns the providers users can sign in with: the environment's first, then by
// name.
func (s *Service) Enabled() []Provider {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Provider, 0, len(s.logins))
	for _, l := range s.logins {
		out = append(out, l.Provider)
	}
	slices.SortFunc(out, func(a, b Provider) int {
		switch {
		case a.ID == EnvID:
			return -1
		case b.ID == EnvID:
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	return out
}

// AnyEnabled reports whether a user can sign in through some provider.
func (s *Service) AnyEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.logins) > 0
}

// Login returns the enabled provider with that id.
func (s *Service) Login(id string) (*Login, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.logins[id]
	return l, ok
}

// Complete runs start (which starts the session of a login l verified) unless the provider
// was changed or taken away since, which is ErrGone. A change waits for start, and so
// revokes the session it starts.
func (s *Service) Complete(l *Login, start func() error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.logins[l.Provider.ID] != l {
		return ErrGone
	}
	return start()
}

// LogoutURL is the sign-out page of the enabled provider with that issuer, if any.
func (s *Service) LogoutURL(issuer string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.logins {
		if l.Provider.Issuer == issuer {
			return l.Client.LogoutURL()
		}
	}
	return ""
}

// Create adds a provider.
func (s *Service) Create(ctx context.Context, in Input) (Provider, error) {
	if !s.httpsPublicURL() {
		return Provider{}, &InputError{Msg: "single sign-on needs an https:// public URL (NUDGE_PUBLIC_URL)"}
	}
	if in.ID == "" {
		in.ID = DeriveID(in.Name)
	}
	if !validID(in.ID) {
		return Provider{}, &InputError{Msg: "id must be 1 to 40 lowercase letters, digits or dashes, and not start with a dash"}
	}
	if in.ID == EnvID {
		return Provider{}, ErrTaken
	}
	if err := validate(&in); err != nil {
		return Provider{}, err
	}
	if in.ClientSecret == nil {
		return Provider{}, &InputError{Msg: "client_secret is required"}
	}
	enabled := in.Enabled == nil || *in.Enabled

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Provider{}, fmt.Errorf("create provider: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := s.rows(ctx, tx)
	if err != nil {
		return Provider{}, err
	}
	if len(rows) >= MaxProviders {
		return Provider{}, &InputError{Msg: fmt.Sprintf("at most %d providers", MaxProviders)}
	}
	if err := s.conflict(rows, in, ""); err != nil {
		return Provider{}, err
	}
	now := s.now().Unix()
	sealed := s.box.Seal([]byte(*in.ClientSecret), purpose(in.ID, in.Issuer))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO oidc_providers (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ID, in.Name, in.Issuer, in.ClientID, sealed, in.AdminRole, in.UserRole, in.RequireMFA,
		int64(in.SessionTTL/time.Second), enabled, now, now); err != nil {
		return Provider{}, fmt.Errorf("create provider: %w", err)
	}
	r, err := s.scan(tx.QueryRowContext(ctx, `SELECT `+columns+` FROM oidc_providers WHERE id = ?`, in.ID))
	if err != nil {
		return Provider{}, fmt.Errorf("create provider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Provider{}, fmt.Errorf("create provider: %w", err)
	}
	s.install(r)
	return r.Provider, nil
}

// Change is the outcome of an update or delete.
type Change struct {
	Provider Provider
	// Revoked is what the users of a provider taken away lost.
	Revoked auth.IssuerRevoked
}

// Update replaces a provider's settings. Taking the provider away (Enabled false, or
// another issuer) revokes its users' sessions and API tokens. A new issuer or client ID
// needs the secret again, so a changed issuer can't be used to get the stored one sent
// elsewhere. Other changes apply to the next login only.
func (s *Service) Update(ctx context.Context, id string, in Input) (Change, error) {
	if id == EnvID && s.env != nil {
		return Change{}, ErrReadOnly
	}
	if err := validate(&in); err != nil {
		return Change{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Change{}, fmt.Errorf("update provider: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := s.rows(ctx, tx)
	if err != nil {
		return Change{}, err
	}
	at := slices.IndexFunc(rows, func(r row) bool { return r.ID == id })
	if at < 0 {
		return Change{}, ErrNotFound
	}
	cur := rows[at]
	if err := s.conflict(rows, in, id); err != nil {
		return Change{}, err
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if enabled && !s.httpsPublicURL() {
		return Change{}, &InputError{Msg: "single sign-on needs an https:// public URL (NUDGE_PUBLIC_URL)"}
	}
	sealed := cur.sealed
	switch {
	case in.ClientSecret != nil:
		sealed = s.box.Seal([]byte(*in.ClientSecret), purpose(id, in.Issuer))
	case in.Issuer != cur.Issuer || in.ClientID != cur.ClientID:
		return Change{}, &InputError{Msg: "client_secret is required when the issuer or client ID changes"}
	}
	var change Change
	if cur.Enabled && (!enabled || in.Issuer != cur.Issuer) {
		after := slices.Clone(rows)
		after[at].Enabled, after[at].Issuer = enabled, in.Issuer
		if change.Revoked, err = s.takeAway(ctx, tx, cur.Issuer, after); err != nil {
			return Change{}, err
		}
	}
	r, err := s.scan(tx.QueryRowContext(ctx, `
		UPDATE oidc_providers SET name = ?, issuer = ?, client_id = ?, client_secret = ?, admin_role = ?, user_role = ?,
			require_mfa = ?, session_ttl_seconds = ?, enabled = ?, updated_at = ?
		WHERE id = ?
		RETURNING `+columns,
		in.Name, in.Issuer, in.ClientID, sealed, in.AdminRole, in.UserRole, in.RequireMFA,
		int64(in.SessionTTL/time.Second), enabled, s.now().Unix(), id))
	if err != nil {
		return Change{}, fmt.Errorf("update provider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Change{}, fmt.Errorf("update provider: %w", err)
	}
	s.install(r)
	change.Provider = r.Provider
	return change, nil
}

// Delete removes a provider, revoking its users' sessions and API tokens as disabling does.
func (s *Service) Delete(ctx context.Context, id string) (Change, error) {
	if id == EnvID && s.env != nil {
		return Change{}, ErrReadOnly
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Change{}, fmt.Errorf("delete provider: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := s.rows(ctx, tx)
	if err != nil {
		return Change{}, err
	}
	at := slices.IndexFunc(rows, func(r row) bool { return r.ID == id })
	if at < 0 {
		return Change{}, ErrNotFound
	}
	change := Change{Provider: rows[at].Provider}
	if rows[at].Enabled {
		if change.Revoked, err = s.takeAway(ctx, tx, rows[at].Issuer, slices.Delete(slices.Clone(rows), at, at+1)); err != nil {
			return Change{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_providers WHERE id = ?`, id); err != nil {
		return Change{}, fmt.Errorf("delete provider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Change{}, fmt.Errorf("delete provider: %w", err)
	}
	delete(s.logins, id)
	return change, nil
}

// takeAway refuses a change that leaves no admin able to sign in and otherwise revokes the
// users of issuer, unless a provider still enabled afterwards (after) serves it.
func (s *Service) takeAway(ctx context.Context, tx *sql.Tx, issuer string, after []row) (auth.IssuerRevoked, error) {
	var issuers []string
	if s.env != nil {
		issuers = append(issuers, s.env.Provider.Issuer)
	}
	for _, r := range after {
		if r.Enabled {
			issuers = append(issuers, r.Issuer)
		}
	}
	if s.pwOff && len(issuers) == 0 {
		return auth.IssuerRevoked{}, ErrLastProvider
	}
	ok, err := s.auth.AdminCanSignIn(ctx, tx, issuers)
	if err != nil {
		return auth.IssuerRevoked{}, err
	}
	if !ok {
		return auth.IssuerRevoked{}, ErrNoAdminLeft
	}
	if slices.Contains(issuers, issuer) {
		return auth.IssuerRevoked{}, nil
	}
	return auth.RevokeIssuer(ctx, tx, issuer)
}

// conflict reports a clash of in with another provider than self. Issuers count as the
// same with or without a trailing slash.
func (s *Service) conflict(rows []row, in Input, self string) error {
	same := func(name, issuer string) bool {
		return strings.EqualFold(name, in.Name) || strings.TrimRight(issuer, "/") == strings.TrimRight(in.Issuer, "/")
	}
	if s.env != nil && same(s.env.Provider.Name, s.env.Provider.Issuer) {
		return ErrTaken
	}
	for _, r := range rows {
		if r.ID == self {
			continue
		}
		if r.ID == in.ID || same(r.Name, r.Issuer) {
			return ErrTaken
		}
	}
	return nil
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func validID(id string) bool { return len(id) <= maxID && idPattern.MatchString(id) }

// DeriveID turns a name into a provider id: lowercase letters and digits, dashes between.
func DeriveID(name string) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(c)
		default:
			dash = true
		}
	}
	id := b.String()
	if len(id) > maxID {
		id = strings.TrimRight(id[:maxID], "-")
	}
	return id
}

// validate checks in and fills in its defaults.
func validate(in *Input) error {
	in.Name, in.Issuer, in.ClientID = strings.TrimSpace(in.Name), strings.TrimSpace(in.Issuer), strings.TrimSpace(in.ClientID)
	in.AdminRole, in.UserRole = strings.TrimSpace(in.AdminRole), strings.TrimSpace(in.UserRole)
	for _, f := range []struct {
		field, value string
		maxLen       int
	}{
		{"name", in.Name, maxName},
		{"client_id", in.ClientID, maxClientID},
		{"admin_role", in.AdminRole, maxRole},
	} {
		if f.value == "" {
			return &InputError{Msg: f.field + " is required"}
		}
		if err := validText(f.field, f.value, f.maxLen); err != nil {
			return err
		}
	}
	if err := validText("user_role", in.UserRole, maxRole); err != nil {
		return err
	}
	if in.UserRole == in.AdminRole {
		return &InputError{Msg: "user_role must differ from admin_role"}
	}
	if len(in.Issuer) > maxIssuer {
		return &InputError{Msg: "issuer is too long"}
	}
	u, err := url.Parse(in.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return &InputError{Msg: "issuer must be an https:// URL"}
	}
	if in.ClientSecret != nil && (*in.ClientSecret == "" || len(*in.ClientSecret) > maxSecret) {
		return &InputError{Msg: fmt.Sprintf("client_secret must be 1 to %d bytes", maxSecret)}
	}
	if in.SessionTTL == 0 {
		in.SessionTTL = DefaultSessionTTL
	}
	if in.SessionTTL < MinSessionTTL || in.SessionTTL > MaxSessionTTL {
		return &InputError{Msg: "session TTL must be between 5 minutes and 30 days"}
	}
	return nil
}

func validText(field, s string, maxLen int) error {
	if len(s) > maxLen {
		return &InputError{Msg: fmt.Sprintf("%s is longer than %d bytes", field, maxLen)}
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return &InputError{Msg: field + " has control characters"}
	}
	return nil
}
