package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// TokenPrefix starts every API token, so leaked tokens are easy to recognise.
	TokenPrefix = "nudge_"
	// tokenShownLen is how much of a token lists show: the prefix and 4 random characters.
	tokenShownLen = len(TokenPrefix) + 4
	maxTokenName  = 64
	// maxTokensPerUser bounds the rows one user can create.
	maxTokensPerUser = 100
)

var (
	// ErrTokenNotFound means the user has no token with that id.
	ErrTokenNotFound = errors.New("token not found")
	// ErrTokenExists means the user already has a token with that name.
	ErrTokenExists = errors.New("a token with that name already exists")
	// ErrTooManyTokens means the user has maxTokensPerUser tokens.
	ErrTooManyTokens = fmt.Errorf("at most %d tokens per user", maxTokensPerUser)
)

// Token describes an API token; the token itself is shown only once, at creation.
type Token struct {
	ID        int64
	Name      string
	Prefix    string
	CreatedAt time.Time
	// LastUsedAt is nil until the token is used; it is updated at most once a minute.
	LastUsedAt *time.Time
}

// CreateToken creates a named token for the user and returns it with its secret value.
func (s *Service) CreateToken(ctx context.Context, userID int64, name string) (Token, string, error) {
	if err := validTokenName(name); err != nil {
		return Token{}, "", err
	}
	secret := TokenPrefix + rand.Text()
	now := s.now().Unix()
	// One statement, so concurrent creates can't pass the limit together.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO api_tokens (user_id, name, prefix, token_hash, created_at)
		SELECT ?, ?, ?, ?, ?
		WHERE (SELECT COUNT(*) FROM api_tokens WHERE user_id = ?) < ?
		ON CONFLICT (user_id, name) DO NOTHING`,
		userID, name, secret[:tokenShownLen], tokenHash(secret), now, userID, maxTokensPerUser)
	if err != nil {
		return Token{}, "", fmt.Errorf("create token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Token{}, "", fmt.Errorf("create token: %w", err)
	}
	if n == 0 {
		var exists bool
		err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM api_tokens WHERE user_id = ? AND name = ?)`, userID, name).Scan(&exists)
		switch {
		case err != nil:
			return Token{}, "", fmt.Errorf("create token: %w", err)
		case exists:
			return Token{}, "", ErrTokenExists
		default:
			return Token{}, "", ErrTooManyTokens
		}
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Token{}, "", fmt.Errorf("create token: %w", err)
	}
	return Token{ID: id, Name: name, Prefix: secret[:tokenShownLen], CreatedAt: time.Unix(now, 0)}, secret, nil
}

// ListTokens returns the user's tokens, oldest first.
func (s *Service) ListTokens(ctx context.Context, userID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, prefix, created_at, last_used_at FROM api_tokens
		WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	tokens := []Token{}
	for rows.Next() {
		var t Token
		var created int64
		var lastUsed sql.NullInt64
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &created, &lastUsed); err != nil {
			return nil, fmt.Errorf("list tokens: %w", err)
		}
		t.CreatedAt = time.Unix(created, 0)
		if lastUsed.Valid {
			lu := time.Unix(lastUsed.Int64, 0)
			t.LastUsedAt = &lu
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	return tokens, nil
}

// DeleteToken revokes one of the user's tokens.
func (s *Service) DeleteToken(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	if n == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// TokenUser returns the owner of a valid API token, or ErrNoSession.
func (s *Service) TokenUser(ctx context.Context, secret string) (User, error) {
	if !strings.HasPrefix(secret, TokenPrefix) {
		return User{}, ErrNoSession
	}
	var u User
	var id, created int64
	var lastUsed, expires sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.name, t.last_used_at, t.expires_at, u.id, u.username, u.created_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id
		WHERE t.token_hash = ?`, tokenHash(secret)).
		Scan(&id, &u.Token, &lastUsed, &expires, &u.ID, &u.Username, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, fmt.Errorf("look up token: %w", err)
	}
	now := s.now().Unix()
	if expires.Valid && now >= expires.Int64 {
		return User{}, ErrNoSession
	}
	if !lastUsed.Valid || now-lastUsed.Int64 >= int64(touchInterval/time.Second) {
		// Best effort, like a session's last_seen_at.
		if _, err := s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now, id); err != nil {
			s.log.Warn("touch token", "err", err)
		}
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, nil
}

func validTokenName(name string) error {
	if name == "" || len(name) > maxTokenName {
		return &InputError{Msg: fmt.Sprintf("name must be 1 to %d bytes", maxTokenName)}
	}
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return &InputError{Msg: "name must be valid UTF-8 without control characters or surrounding spaces"}
	}
	return nil
}
