// Package auth holds nudge's users and browser sessions: argon2id password hashes,
// first-run setup, login with rate limiting, and session tokens stored as SHA-256 hashes.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultSessionTTL is how long a session lasts after login.
const DefaultSessionTTL = 7 * 24 * time.Hour

// AdminUsername is the user NUDGE_ADMIN_PASSWORD creates.
const AdminUsername = "admin"

const (
	minPasswordLen = 8
	maxPasswordLen = 1024
	maxUsernameLen = 64
	// touchInterval throttles last_seen_at writes on session lookups.
	touchInterval = time.Minute
	// maxConcurrentHashes bounds argon2id's memory use under a login flood.
	maxConcurrentHashes = 4

	ipFailureLimit   = 20
	userFailureLimit = 10
	failureWindow    = 15 * time.Minute
	maxLimiterKeys   = 10000
)

var (
	// ErrInvalidCredentials is returned for an unknown user or a wrong password alike.
	ErrInvalidCredentials = errors.New("invalid username or password")
	// ErrSetupDone means a user already exists.
	ErrSetupDone = errors.New("setup is already done")
	// ErrNoSession means the token is unknown or expired.
	ErrNoSession = errors.New("no valid session")
)

// InputError is a rejected username or password; its message is safe to show.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// RateLimitedError means too many failed logins from this IP or for this username.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string { return "too many failed login attempts" }

// User is an account.
type User struct {
	ID        int64
	Username  string
	CreatedAt time.Time
}

// Session is a new login. Token is the cookie value; only its hash is stored.
type Session struct {
	Token     string
	User      User
	ExpiresAt time.Time
}

// Options are the Service's dependencies; zero values get defaults.
type Options struct {
	DB  *sql.DB
	Log *slog.Logger
	// Now is the clock (time.Now by default).
	Now func() time.Time
	// Params are the argon2id costs of new hashes (DefaultParams by default).
	Params     Params
	SessionTTL time.Duration
}

// Service manages users and sessions in the store's database.
type Service struct {
	db     *sql.DB
	log    *slog.Logger
	now    func() time.Time
	params Params
	ttl    time.Duration

	hashes    chan struct{}
	ipLimit   *limiter
	userLimit *limiter

	dummyOnce sync.Once
	dummyHash string
	dummyErr  error
}

// New returns a Service.
func New(opts Options) *Service {
	s := &Service{
		db:        opts.DB,
		log:       opts.Log,
		now:       opts.Now,
		params:    opts.Params,
		ttl:       opts.SessionTTL,
		hashes:    make(chan struct{}, maxConcurrentHashes),
		ipLimit:   newLimiter(ipFailureLimit, failureWindow, maxLimiterKeys),
		userLimit: newLimiter(userFailureLimit, failureWindow, maxLimiterKeys),
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.params == (Params{}) {
		s.params = DefaultParams
	}
	if s.ttl == 0 {
		s.ttl = DefaultSessionTTL
	}
	return s
}

// SetupNeeded reports whether no user exists yet.
func (s *Service) SetupNeeded(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("count users: %w", err)
	}
	return !exists, nil
}

// CreateFirstUser creates the admin while no user exists. The check and the insert are
// one statement, so concurrent calls create exactly one user; the others get
// ErrSetupDone.
func (s *Service) CreateFirstUser(ctx context.Context, username, password string) (User, error) {
	if err := validUsername(username); err != nil {
		return User{}, err
	}
	if err := validPassword(password); err != nil {
		return User{}, err
	}
	if needed, err := s.SetupNeeded(ctx); err != nil {
		return User{}, err
	} else if !needed {
		// Skips the hash; the insert below still decides a race.
		return User{}, ErrSetupDone
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return User{}, err
	}
	now := s.now().Unix()
	u := User{Username: username, CreatedAt: time.Unix(now, 0)}
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO users (username, password_hash, created_at, updated_at)
		SELECT ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)
		RETURNING id`, username, hash, now, now).Scan(&u.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrSetupDone
	}
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// EnsureAdmin creates the user AdminUsername with password if no user exists yet, and
// reports whether it did.
func (s *Service) EnsureAdmin(ctx context.Context, password string) (bool, error) {
	_, err := s.CreateFirstUser(ctx, AdminUsername, password)
	if errors.Is(err, ErrSetupDone) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create admin: %w", err)
	}
	return true, nil
}

// Login checks the credentials and starts a session. ip and username are rate-limited
// separately; an unknown user costs as much time as a wrong password.
func (s *Service) Login(ctx context.Context, ip, username, password string) (Session, error) {
	now := s.now()
	userKey := strings.ToLower(username)
	if d, blocked := s.ipLimit.blocked(ip, now); blocked {
		return Session{}, &RateLimitedError{RetryAfter: d}
	}
	if len(username) > maxUsernameLen || len(password) > maxPasswordLen {
		// No such user can exist; don't let long keys into the limiter.
		s.ipLimit.fail(ip, now)
		return Session{}, ErrInvalidCredentials
	}
	if d, blocked := s.userLimit.blocked(userKey, now); blocked {
		return Session{}, &RateLimitedError{RetryAfter: d}
	}

	var u User
	var hash string
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, password_hash, created_at FROM users WHERE username = ?`, username,
	).Scan(&u.ID, &u.Username, &hash, &created)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("look up user: %w", err)
	}
	if !known {
		if hash, err = s.dummy(); err != nil {
			return Session{}, err
		}
	}
	ok, err := s.verify(ctx, hash, password)
	if err != nil {
		return Session{}, err
	}
	if !known || !ok {
		s.ipLimit.fail(ip, now)
		s.userLimit.fail(userKey, now)
		return Session{}, ErrInvalidCredentials
	}
	s.userLimit.reset(userKey)
	u.CreatedAt = time.Unix(created, 0)
	return s.newSession(ctx, u)
}

func (s *Service) newSession(ctx context.Context, u User) (Session, error) {
	token := rand.Text()
	now := s.now()
	expires := now.Add(s.ttl)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, user_id, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		tokenHash(token), u.ID, now.Unix(), now.Unix(), expires.Unix())
	if err != nil {
		return Session{}, fmt.Errorf("create session: %w", err)
	}
	return Session{Token: token, User: u, ExpiresAt: time.Unix(expires.Unix(), 0)}, nil
}

// SessionUser returns the user of a live session. An expired session is deleted.
func (s *Service) SessionUser(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	id := tokenHash(token)
	var u User
	var created, lastSeen, expires int64
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.created_at, s.last_seen_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ?`, id).Scan(&u.ID, &u.Username, &created, &lastSeen, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, fmt.Errorf("look up session: %w", err)
	}
	now := s.now().Unix()
	if now >= expires {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, id); err != nil {
			return User{}, fmt.Errorf("delete expired session: %w", err)
		}
		return User{}, ErrNoSession
	}
	if now-lastSeen >= int64(touchInterval/time.Second) {
		if _, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, now, id); err != nil {
			return User{}, fmt.Errorf("touch session: %w", err)
		}
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

// Logout ends the session of token, if there is one.
func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, tokenHash(token)); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Cleanup deletes expired sessions and forgets finished rate-limit windows.
func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	now := s.now()
	s.ipLimit.prune(now)
	s.userLimit.prune(now)
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return n, nil
}

// RunCleanup calls Cleanup every interval until ctx is cancelled.
func (s *Service) RunCleanup(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.Cleanup(ctx)
			if err != nil {
				if ctx.Err() == nil {
					s.log.Warn("session cleanup", "err", err)
				}
				continue
			}
			if n > 0 {
				s.log.Debug("deleted expired sessions", "count", n)
			}
		}
	}
}

func (s *Service) hash(ctx context.Context, password string) (string, error) {
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	return hashPassword(password, s.params)
}

func (s *Service) verify(ctx context.Context, hash, password string) (bool, error) {
	if err := s.acquire(ctx); err != nil {
		return false, err
	}
	defer s.release()
	ok, err := verifyPassword(hash, password)
	if err != nil {
		return false, fmt.Errorf("verify password: %w", err)
	}
	return ok, nil
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.hashes <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for password hashing: %w", ctx.Err())
	}
}

func (s *Service) release() { <-s.hashes }

// dummy is a hash with the current params to verify against for unknown users.
func (s *Service) dummy() (string, error) {
	s.dummyOnce.Do(func() {
		s.dummyHash, s.dummyErr = hashPassword(rand.Text(), s.params)
	})
	return s.dummyHash, s.dummyErr
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validUsername(name string) error {
	if name == "" || len(name) > maxUsernameLen {
		return &InputError{Msg: fmt.Sprintf("username must be 1 to %d bytes", maxUsernameLen)}
	}
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return &InputError{Msg: "username must be valid UTF-8 without leading or trailing spaces"}
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return &InputError{Msg: "username must not contain control characters"}
		}
	}
	return nil
}

func validPassword(pw string) error {
	if utf8.RuneCountInString(pw) < minPasswordLen || len(pw) > maxPasswordLen {
		return &InputError{Msg: fmt.Sprintf("password must be at least %d characters and at most %d bytes", minPasswordLen, maxPasswordLen)}
	}
	return nil
}
