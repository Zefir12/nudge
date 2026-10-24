package environments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The secrets start with a prefix, so leaked ones are easy to recognise; both carry 32
// random bytes.
const (
	// JoinTokenPrefix starts join tokens.
	JoinTokenPrefix = "nudge_join_"
	// AgentSecretPrefix starts agent secrets.
	AgentSecretPrefix = "nudge_agent_"
)

const (
	agentIDPrefix = "agt_"
	// JoinTokenTTL is how long a join token can be used.
	JoinTokenTTL = 24 * time.Hour
	maxName      = 64
)

var (
	// ErrNameTaken means another environment has that name.
	ErrNameTaken = errors.New("an environment with that name already exists")
	// ErrNotAgent means the environment is not an agent environment.
	ErrNotAgent = errors.New("not an agent environment")
	// ErrInvalidJoinToken covers unknown, used and expired join tokens alike.
	ErrInvalidJoinToken = errors.New("invalid or expired join token")
	// ErrInvalidCredentials means the agent ID and secret don't match an environment.
	ErrInvalidCredentials = errors.New("invalid agent credentials")
)

// InputError is a validation error whose message can be shown to the user.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// JoinToken is a one-time token an agent exchanges for its credentials.
type JoinToken struct {
	Token     string
	ExpiresAt time.Time
}

// Credentials identify an enrolled agent; the secret is shown only once.
type Credentials struct {
	EnvironmentID int64
	AgentID       string
	Secret        string
}

// CreateAgent creates an agent environment named name and its first join token.
func (s *Service) CreateAgent(ctx context.Context, name string) (Environment, JoinToken, error) {
	if err := validName(name); err != nil {
		return Environment{}, JoinToken{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Environment{}, JoinToken{}, fmt.Errorf("create agent environment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	res, err := tx.ExecContext(ctx, `INSERT INTO environments (name, kind, created_at) VALUES (?, ?, ?) ON CONFLICT (name) DO NOTHING`,
		name, KindAgent, now.Unix())
	if err != nil {
		return Environment{}, JoinToken{}, fmt.Errorf("create agent environment: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Environment{}, JoinToken{}, fmt.Errorf("create agent environment: %w", err)
	} else if n == 0 {
		return Environment{}, JoinToken{}, ErrNameTaken
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Environment{}, JoinToken{}, fmt.Errorf("create agent environment: %w", err)
	}
	jt, err := s.putJoinToken(ctx, tx, id)
	if err != nil {
		return Environment{}, JoinToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return Environment{}, JoinToken{}, fmt.Errorf("create agent environment: %w", err)
	}
	return Environment{ID: id, Name: name, Kind: KindAgent, CreatedAt: time.Unix(now.Unix(), 0)}, jt, nil
}

// RegenerateJoinToken replaces the agent environment's join token, for an agent that
// never enrolled or lost its credentials. The current credentials stay valid until an
// agent enrolls with the new token.
func (s *Service) RegenerateJoinToken(ctx context.Context, envID int64) (JoinToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return JoinToken{}, fmt.Errorf("regenerate join token: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAgent(ctx, tx, envID); err != nil {
		return JoinToken{}, err
	}
	jt, err := s.putJoinToken(ctx, tx, envID)
	if err != nil {
		return JoinToken{}, err
	}
	if err := tx.Commit(); err != nil {
		return JoinToken{}, fmt.Errorf("regenerate join token: %w", err)
	}
	return jt, nil
}

// Revoke deletes the agent environment's credentials and pending join token, so its agent
// can no longer connect. The environment stays; a new join token re-enrolls it.
func (s *Service) Revoke(ctx context.Context, envID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("revoke agent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := requireAgent(ctx, tx, envID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE environments SET agent_id = NULL, agent_secret_hash = NULL WHERE id = ?`, envID); err != nil {
		return fmt.Errorf("revoke agent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM join_tokens WHERE environment_id = ?`, envID); err != nil {
		return fmt.Errorf("revoke agent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("revoke agent: %w", err)
	}
	return nil
}

// Enroll exchanges a join token for new agent credentials. The token works once and only
// before it expires; enrolling replaces any credentials the environment had.
func (s *Service) Enroll(ctx context.Context, token string) (Credentials, error) {
	if !strings.HasPrefix(token, JoinTokenPrefix) {
		return Credentials{}, ErrInvalidJoinToken
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Credentials{}, fmt.Errorf("enroll agent: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Deleting first makes the token single use even under concurrent enrolls.
	var envID, expires int64
	err = tx.QueryRowContext(ctx, `DELETE FROM join_tokens WHERE token_hash = ? RETURNING environment_id, expires_at`,
		hashSecret(token)).Scan(&envID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Credentials{}, ErrInvalidJoinToken
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("enroll agent: %w", err)
	}
	if !s.now().Before(time.Unix(expires, 0)) {
		// Commit the delete: an expired token is of no further use.
		if err := tx.Commit(); err != nil {
			return Credentials{}, fmt.Errorf("enroll agent: %w", err)
		}
		return Credentials{}, ErrInvalidJoinToken
	}
	creds := Credentials{EnvironmentID: envID, AgentID: agentIDPrefix + rand.Text(), Secret: AgentSecretPrefix + randomSecret()}
	if _, err := tx.ExecContext(ctx, `UPDATE environments SET agent_id = ?, agent_secret_hash = ? WHERE id = ?`,
		creds.AgentID, hashSecret(creds.Secret), envID); err != nil {
		return Credentials{}, fmt.Errorf("enroll agent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Credentials{}, fmt.Errorf("enroll agent: %w", err)
	}
	return creds, nil
}

// AuthenticateAgent returns the environment whose agent has these credentials.
func (s *Service) AuthenticateAgent(ctx context.Context, agentID, secret string) (Environment, error) {
	var stored sql.NullString
	var envID int64
	err := s.db.QueryRowContext(ctx, `SELECT id, agent_secret_hash FROM environments WHERE agent_id = ? AND kind = ?`, agentID, KindAgent).
		Scan(&envID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return Environment{}, ErrInvalidCredentials
	}
	if err != nil {
		return Environment{}, fmt.Errorf("authenticate agent: %w", err)
	}
	if !stored.Valid || subtle.ConstantTimeCompare([]byte(stored.String), []byte(hashSecret(secret))) != 1 {
		return Environment{}, ErrInvalidCredentials
	}
	return s.Get(ctx, envID)
}

// putJoinToken stores a new join token for the environment, replacing a pending one.
func (s *Service) putJoinToken(ctx context.Context, tx *sql.Tx, envID int64) (JoinToken, error) {
	jt := JoinToken{Token: JoinTokenPrefix + randomSecret(), ExpiresAt: time.Unix(s.now().Add(JoinTokenTTL).Unix(), 0)}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO join_tokens (environment_id, token_hash, expires_at) VALUES (?, ?, ?)
		ON CONFLICT (environment_id) DO UPDATE SET token_hash = excluded.token_hash, expires_at = excluded.expires_at`,
		envID, hashSecret(jt.Token), jt.ExpiresAt.Unix()); err != nil {
		return JoinToken{}, fmt.Errorf("store join token: %w", err)
	}
	return jt, nil
}

func requireAgent(ctx context.Context, tx *sql.Tx, envID int64) error {
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT kind FROM environments WHERE id = ?`, envID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get environment %d: %w", envID, err)
	}
	if kind != KindAgent {
		return ErrNotAgent
	}
	return nil
}

// randomSecret is 32 random bytes, hex encoded.
func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func validName(name string) error {
	if name == "" || len(name) > maxName {
		return &InputError{Msg: fmt.Sprintf("name must be 1 to %d bytes", maxName)}
	}
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return &InputError{Msg: "name must be valid UTF-8 without control characters or surrounding spaces"}
	}
	return nil
}
