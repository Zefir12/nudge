package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// maxProfileLen caps the display name and email kept from the provider.
const maxProfileLen = 256

// Identity is a user the SSO provider vouched for.
type Identity struct {
	Issuer  string
	Subject string
	// Username is the provider's preferred username, a hint for a new user's name.
	Username    string
	DisplayName string
	Email       string
}

// SSOLogin starts a session for id's user, found by issuer and subject only and created
// on the first login. A local user with the same name or email is never linked: a new
// user gets a name no one has (and never AdminUsername, the break-glass user). The
// display name and email are refreshed on every login. The session lasts SSOSessionTTL.
func (s *Service) SSOLogin(ctx context.Context, id Identity) (Session, error) {
	if id.Issuer == "" || id.Subject == "" {
		return Session{}, errors.New("sso login: missing issuer or subject")
	}
	display, email := clipProfile(id.DisplayName), clipProfile(id.Email)
	u, found, err := s.refreshSSOUser(ctx, id, display, email)
	if err != nil {
		return Session{}, err
	}
	for _, name := range usernameCandidates(id) {
		if found {
			break
		}
		now := s.now().Unix()
		var created int64
		err := s.db.QueryRowContext(ctx, `
			INSERT INTO users (username, password_hash, created_at, updated_at, oidc_issuer, oidc_subject, display_name, email)
			VALUES (?, '', ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING
			RETURNING id, username, created_at`,
			name, now, now, id.Issuer, id.Subject, display, email,
		).Scan(&u.ID, &u.Username, &created)
		switch {
		case err == nil:
			u.CreatedAt = time.Unix(created, 0)
			found = true
			s.log.Info("sso user created", "username", u.Username, "subject", id.Subject)
		case !errors.Is(err, sql.ErrNoRows):
			return Session{}, fmt.Errorf("create sso user: %w", err)
		default:
			// The name is taken, or a concurrent first login created this user.
			if u, found, err = s.refreshSSOUser(ctx, id, display, email); err != nil {
				return Session{}, err
			}
		}
	}
	if !found {
		return Session{}, errors.New("create sso user: no free username")
	}
	u.SSO = true
	return s.newSession(ctx, u, s.ssoTTL)
}

// refreshSSOUser updates the profile of id's user and returns it, if it exists.
func (s *Service) refreshSSOUser(ctx context.Context, id Identity, display, email string) (User, bool, error) {
	var u User
	var created int64
	err := s.db.QueryRowContext(ctx, `
		UPDATE users SET display_name = ?, email = ?, updated_at = ?
		WHERE oidc_issuer = ? AND oidc_subject = ?
		RETURNING id, username, created_at`,
		display, email, s.now().Unix(), id.Issuer, id.Subject,
	).Scan(&u.ID, &u.Username, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("update sso user: %w", err)
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, true, nil
}

// usernameCandidates are the names to try for a new SSO user: the preferred username,
// then the email, each with a few numbered variants, then one derived from the subject
// and a random last resort.
func usernameCandidates(id Identity) []string {
	var out []string
	for _, base := range []string{id.Username, id.Email} {
		base = strings.TrimSpace(base)
		if validUsername(base) != nil || strings.EqualFold(base, AdminUsername) {
			continue
		}
		out = append(out, base)
		for i := 2; i <= 5; i++ {
			if n := fmt.Sprintf("%s-%d", base, i); len(n) <= maxUsernameLen {
				out = append(out, n)
			}
		}
	}
	sum := sha256.Sum256([]byte(id.Issuer + "\x00" + id.Subject))
	return append(out, "sso-"+hex.EncodeToString(sum[:6]), "sso-"+strings.ToLower(rand.Text()[:12]))
}

// clipProfile keeps at most maxProfileLen bytes of valid UTF-8.
func clipProfile(s string) string {
	s = strings.ToValidUTF8(strings.TrimSpace(s), "")
	for len(s) > maxProfileLen {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}
