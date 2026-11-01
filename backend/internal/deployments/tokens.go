package deployments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// TokenPrefix starts every deploy token. API tokens are "nudge_" and uppercase
	// base32, so the API tells the two apart before any lookup.
	TokenPrefix = "nudge_dt_"
	// tokenShownLen is how much of a token lists show.
	tokenShownLen = len(TokenPrefix) + 4
	maxTokenName  = 64
	// maxTokens bounds the tokens of one deployment.
	maxTokens = 20
	// touchInterval throttles last_used_at writes.
	touchInterval = time.Minute
)

var (
	// ErrInvalidToken means the token is unknown, expired or for another deployment.
	ErrInvalidToken = errors.New("invalid deploy token")
	// ErrScope means the token is valid but lacks the scope.
	ErrScope = errors.New("the deploy token's scopes don't allow that")
	// ErrTokenExists means the deployment has a token with that name.
	ErrTokenExists = errors.New("a token with that name already exists")
)

// Scope is what a deploy token may do on its deployment.
type Scope string

const (
	// ScopeDeploy starts runs.
	ScopeDeploy Scope = "deploy"
	// ScopeRunsRead reads runs.
	ScopeRunsRead Scope = "runs:read"
)

// Scopes are the known scopes, in the order they are listed.
var Scopes = []Scope{ScopeDeploy, ScopeRunsRead}

// Token describes a deploy token; its value is shown only once, at creation.
type Token struct {
	ID        int64
	Name      string
	Prefix    string
	Scopes    []Scope
	CreatedBy string
	// CreatorID is the creator's user ID, CreatorRole their role on the deployment now
	// ("" if they aren't a member); set by AuthenticateToken.
	CreatorID   int64
	CreatorRole Role
	// CreatorAdmin says the creator is a nudge admin now.
	CreatorAdmin bool
	CreatedAt    time.Time
	// ExpiresAt is nil for a token that doesn't expire.
	ExpiresAt *time.Time
	// LastUsedAt is nil until the token is used; it is updated at most once a minute.
	LastUsedAt *time.Time
}

// Has reports whether the token has scope sc.
func (t Token) Has(sc Scope) bool { return slices.Contains(t.Scopes, sc) }

// TokenInput is a new deploy token.
type TokenInput struct {
	Name   string
	Scopes []Scope
	// ExpiresAt, if set, must be in the future.
	ExpiresAt *time.Time
}

func (s *Service) validToken(in *TokenInput) error {
	if in.Name == "" || len(in.Name) > maxTokenName || !utf8.ValidString(in.Name) ||
		strings.TrimSpace(in.Name) != in.Name || strings.ContainsFunc(in.Name, unicode.IsControl) {
		return &InputError{Msg: fmt.Sprintf("token name must be 1 to %d bytes, without control characters or surrounding spaces", maxTokenName)}
	}
	if len(in.Scopes) == 0 {
		return &InputError{Msg: "a token needs at least one scope"}
	}
	var scopes []Scope
	for _, sc := range Scopes {
		if slices.Contains(in.Scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	for _, sc := range in.Scopes {
		if !slices.Contains(Scopes, sc) {
			return &InputError{Msg: fmt.Sprintf("unknown scope %q", sc)}
		}
	}
	in.Scopes = scopes
	if in.ExpiresAt != nil && !in.ExpiresAt.After(s.clock.Now()) {
		return &InputError{Msg: "expires_at must be in the future"}
	}
	return nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func joinScopes(scopes []Scope) string {
	parts := make([]string, len(scopes))
	for i, sc := range scopes {
		parts[i] = string(sc)
	}
	return strings.Join(parts, " ")
}

func splitScopes(s string) []Scope {
	var out []Scope
	for f := range strings.FieldsSeq(s) {
		out = append(out, Scope(f))
	}
	return out
}

// CreateToken adds a deploy token to the deployment named name and returns it with its
// value.
func (s *Service) CreateToken(ctx context.Context, actor Actor, name string, in TokenInput) (Token, string, error) {
	d, err := s.Authorize(ctx, actor, name, ActionManageTokens)
	if err != nil {
		return Token{}, "", err
	}
	if err := s.validToken(&in); err != nil {
		return Token{}, "", err
	}
	secret := TokenPrefix + rand.Text()
	var expires any
	if in.ExpiresAt != nil {
		expires = in.ExpiresAt.UnixMilli()
	}
	// One statement, so concurrent creates can't pass the bound together.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO deployment_tokens (deployment_id, name, prefix, token_hash, scopes, created_by, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT COUNT(*) FROM deployment_tokens WHERE deployment_id = ?) < ?
		ON CONFLICT (deployment_id, name) DO NOTHING`,
		d.ID, in.Name, secret[:tokenShownLen], tokenHash(secret), joinScopes(in.Scopes), actor.UserID, s.now(), expires, d.ID, maxTokens)
	if err != nil {
		return Token{}, "", fmt.Errorf("create deploy token: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return Token{}, "", fmt.Errorf("create deploy token: %w", err)
		}
		var taken bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deployment_tokens WHERE deployment_id = ? AND name = ?)`, d.ID, in.Name).Scan(&taken); err != nil {
			return Token{}, "", fmt.Errorf("create deploy token: %w", err)
		}
		if taken {
			return Token{}, "", ErrTokenExists
		}
		return Token{}, "", fmt.Errorf("%w: at most %d tokens per deployment", ErrTooMany, maxTokens)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Token{}, "", fmt.Errorf("create deploy token: %w", err)
	}
	t, err := s.token(ctx, d.ID, id)
	return t, secret, err
}

// tokenColumns are what scanToken reads (column names, not a credential).
const tokenColumns = `t.id, t.name, t.prefix, t.scopes, u.username, t.created_at, t.expires_at, t.last_used_at` // #nosec G101

func scanToken(row scanner, extra ...any) (Token, error) {
	var t Token
	var scopes string
	var created int64
	var expires, used sql.NullInt64
	if err := row.Scan(append([]any{&t.ID, &t.Name, &t.Prefix, &scopes, &t.CreatedBy, &created, &expires, &used}, extra...)...); err != nil {
		return Token{}, err
	}
	t.Scopes = splitScopes(scopes)
	t.CreatedAt = time.UnixMilli(created).UTC()
	if expires.Valid {
		e := time.UnixMilli(expires.Int64).UTC()
		t.ExpiresAt = &e
	}
	if used.Valid {
		u := time.UnixMilli(used.Int64).UTC()
		t.LastUsedAt = &u
	}
	return t, nil
}

func (s *Service) token(ctx context.Context, deployment, id int64) (Token, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+` FROM deployment_tokens t
		JOIN users u ON u.id = t.created_by WHERE t.deployment_id = ? AND t.id = ?`, deployment, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("get deploy token: %w", err)
	}
	return t, nil
}

// Tokens returns the deployment's tokens by name.
func (s *Service) Tokens(ctx context.Context, actor Actor, name string) ([]Token, error) {
	d, err := s.Authorize(ctx, actor, name, ActionManageTokens)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenColumns+` FROM deployment_tokens t
		JOIN users u ON u.id = t.created_by WHERE t.deployment_id = ? ORDER BY t.name`, d.ID)
	if err != nil {
		return nil, fmt.Errorf("list deploy tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, fmt.Errorf("list deploy tokens: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deploy tokens: %w", err)
	}
	return out, nil
}

// DeleteToken revokes token id of the deployment named name.
func (s *Service) DeleteToken(ctx context.Context, actor Actor, name string, id int64) error {
	d, err := s.Authorize(ctx, actor, name, ActionManageTokens)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM deployment_tokens WHERE deployment_id = ? AND id = ?`, d.ID, id)
	if err != nil {
		return fmt.Errorf("delete deploy token: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// AuthenticateToken returns the deployment named name and the token secret is, if the
// token belongs to it, hasn't expired and has scope sc. Every mismatch but the scope is
// ErrInvalidToken, so a token can't probe other deployments. A token acts for its
// creator: the caller checks that the creator may still do what it asks (CreatorRole,
// or CreatorAdmin). A disabled creator's tokens are ErrInvalidToken.
func (s *Service) AuthenticateToken(ctx context.Context, name, secret string, sc Scope) (Deployment, Token, error) {
	if !strings.HasPrefix(secret, TokenPrefix) || !ValidName(name) {
		return Deployment{}, Token{}, ErrInvalidToken
	}
	var id, creator int64
	var role string
	var admin bool
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenColumns+`, d.id, t.created_by, COALESCE(m.role, ''), u.role = 'admin' FROM deployment_tokens t
		JOIN users u ON u.id = t.created_by JOIN deployments d ON d.id = t.deployment_id
		LEFT JOIN deployment_members m ON m.deployment_id = d.id AND m.user_id = t.created_by
		WHERE t.token_hash = ? AND d.name = ? AND NOT u.disabled`, tokenHash(secret), name), &id, &creator, &role, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, Token{}, ErrInvalidToken
	}
	if err != nil {
		return Deployment{}, Token{}, fmt.Errorf("look up deploy token: %w", err)
	}
	t.CreatorID, t.CreatorRole, t.CreatorAdmin = creator, Role(role), admin
	now := s.clock.Now()
	if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
		return Deployment{}, Token{}, ErrInvalidToken
	}
	if !t.Has(sc) {
		return Deployment{}, Token{}, ErrScope
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchInterval {
		// Best effort, like an API token's.
		if _, err := s.db.ExecContext(ctx, `UPDATE deployment_tokens SET last_used_at = ? WHERE id = ?`, now.UnixMilli(), t.ID); err != nil {
			s.log.Warn("touch deploy token", "err", err)
		}
	}
	d, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM deployments d
		LEFT JOIN deployment_members m ON 0 WHERE d.id = ?`, id))
	if err != nil {
		return Deployment{}, Token{}, fmt.Errorf("get deployment: %w", err)
	}
	return d, t, nil
}
