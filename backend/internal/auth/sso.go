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
	// Role comes from the provider's roles claim on every login.
	Role Role
}

// ErrDisabled means an admin disabled the SSO user signing in.
var ErrDisabled = errors.New("user is disabled")

// SSOLogin starts a session for id's user, found by issuer and subject only and created
// on the first login. A local user with the same name or email is never linked: a new
// user gets a name no one has (and never AdminUsername, the break-glass user). The
// display name, email and role are refreshed on every login: the role is the provider's,
// not nudge's. A disabled user gets ErrDisabled. The session lasts SSOSessionTTL.
func (s *Service) SSOLogin(ctx context.Context, id Identity) (Session, error) {
	if id.Issuer == "" || id.Subject == "" {
		return Session{}, errors.New("sso login: missing issuer or subject")
	}
	if !id.Role.Valid() {
		return Session{}, fmt.Errorf("sso login: unknown role %q", id.Role)
	}
	display, email := clipProfile(id.DisplayName), clipProfile(id.Email)
	u, found, disabled, err := s.refreshSSOUser(ctx, id, display, email)
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
			INSERT INTO users (username, password_hash, created_at, updated_at, oidc_issuer, oidc_subject, display_name, email, role)
			VALUES (?, '', ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT DO NOTHING
			RETURNING id, username, created_at, role`,
			name, now, now, id.Issuer, id.Subject, display, email, id.Role,
		).Scan(&u.ID, &u.Username, &created, &u.Role)
		switch {
		case err == nil:
			u.CreatedAt = time.Unix(created, 0)
			found = true
			s.log.Info("sso user created", "username", u.Username, "subject", id.Subject)
		case !errors.Is(err, sql.ErrNoRows):
			return Session{}, fmt.Errorf("create sso user: %w", err)
		default:
			// The name is taken, or a concurrent first login created this user.
			if u, found, disabled, err = s.refreshSSOUser(ctx, id, display, email); err != nil {
				return Session{}, err
			}
		}
	}
	if !found {
		return Session{}, errors.New("create sso user: no free username")
	}
	if disabled {
		return Session{}, ErrDisabled
	}
	u.SSO = true
	return s.newSession(ctx, u, s.ssoTTL)
}

// refreshSSOUser updates the profile and role of id's user and returns it, if it exists,
// and whether it is disabled; a disabled user is left as it is. The provider's role
// wins even over the last enabled admin, so a demotion is logged.
func (s *Service) refreshSSOUser(ctx context.Context, id Identity, display, email string) (u User, found, disabled bool, err error) {
	var oldRole Role
	err = s.db.QueryRowContext(ctx, `SELECT id, role, disabled FROM users WHERE oidc_issuer = ? AND oidc_subject = ?`,
		id.Issuer, id.Subject).Scan(&u.ID, &oldRole, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, false, nil
	}
	if err != nil {
		return User{}, false, false, fmt.Errorf("look up sso user: %w", err)
	}
	if disabled {
		return User{}, true, true, nil
	}
	var created int64
	err = s.db.QueryRowContext(ctx, `
		UPDATE users SET display_name = ?, email = ?, role = ?, updated_at = ?
		WHERE id = ?
		RETURNING id, username, created_at, role, disabled`,
		display, email, id.Role, s.now().Unix(), u.ID,
	).Scan(&u.ID, &u.Username, &created, &u.Role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		// Deleted in between: a new user is created.
		return User{}, false, false, nil
	}
	if err != nil {
		return User{}, false, false, fmt.Errorf("update sso user: %w", err)
	}
	if oldRole == RoleAdmin && u.Role != RoleAdmin {
		s.log.Warn("sso user lost the admin role at the provider", "username", u.Username, "subject", id.Subject)
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, true, disabled, nil
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

// Revoked is what RevokeSSO took from an SSO user.
type Revoked struct {
	// Username is the nudge user's name; "" when the provider's user never signed in.
	Username string
	Sessions int64
	Tokens   int64
}

// RevokeSSO ends every session and deletes every API token of the SSO user with this
// issuer and subject, as when the provider takes their role away. The user itself stays,
// so a later login with a role signs them in again. A user that doesn't exist yet
// revokes nothing.
func (s *Service) RevokeSSO(ctx context.Context, issuer, subject string) (Revoked, error) {
	if issuer == "" || subject == "" {
		return Revoked{}, errors.New("revoke sso user: missing issuer or subject")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Revoked{}, fmt.Errorf("revoke sso user: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var r Revoked
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id, username FROM users WHERE oidc_issuer = ? AND oidc_subject = ?`,
		issuer, subject).Scan(&id, &r.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return Revoked{}, nil
	}
	if err != nil {
		return Revoked{}, fmt.Errorf("look up sso user: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
	if err != nil {
		return Revoked{}, fmt.Errorf("revoke sso sessions: %w", err)
	}
	if r.Sessions, err = res.RowsAffected(); err != nil {
		return Revoked{}, fmt.Errorf("revoke sso sessions: %w", err)
	}
	if res, err = tx.ExecContext(ctx, `DELETE FROM api_tokens WHERE user_id = ?`, id); err != nil {
		return Revoked{}, fmt.Errorf("revoke sso tokens: %w", err)
	}
	if r.Tokens, err = res.RowsAffected(); err != nil {
		return Revoked{}, fmt.Errorf("revoke sso tokens: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Revoked{}, fmt.Errorf("revoke sso user: %w", err)
	}
	return r, nil
}

// RelinkRefusedError means the duplicate of a RelinkSSO still owns something; Owns names
// what, so the admin can remove it first. Its message is safe to show.
type RelinkRefusedError struct{ Owns []string }

func (e *RelinkRefusedError) Error() string {
	return "the duplicate still has " + strings.Join(e.Owns, ", ") + "; remove them first"
}

// duplicateOwns are what RelinkSSO refuses to throw away with the duplicate, by name and
// the query counting the duplicate's rows (its id in ?). Sessions are not here: they end.
var duplicateOwns = []struct{ name, query string }{
	{"API tokens", `SELECT COUNT(*) FROM api_tokens WHERE user_id = ?`},
	{"environment access", `SELECT COUNT(*) FROM user_environments WHERE user_id = ?`},
	{"allowances", `SELECT COUNT(*) FROM allowances WHERE user_id = ?`},
	{"pulled images", `SELECT COUNT(*) FROM image_pulls WHERE user_id = ?`},
	{"deployment memberships", `SELECT COUNT(*) FROM deployment_members WHERE user_id = ?`},
	{"deployment tokens", `SELECT COUNT(*) FROM deployment_tokens WHERE created_by = ?`},
	{"deploy webhooks", `SELECT COUNT(*) FROM deploy_hooks WHERE created_by = ?`},
}

// RelinkSSO merges the SSO user duplicate into the SSO user original, as when the
// provider changed and the next login created a new user: in one transaction the
// duplicate is deleted (its sessions end) and the original takes its issuer and subject,
// display name, email and role (the provider's latest), so the next login signs in as the
// original with everything it has. The original's disabled flag stays as it is.
//
// Nothing the duplicate owns is moved: a duplicate with API tokens, environment access,
// allowances, pulled images, deployment memberships or tokens, or deploy webhooks is a
// RelinkRefusedError. (Docker objects carry the owner key of the user who made them and
// can't be relabelled; a user only labels them while it has environment access.)
// Merging a user into itself, or a local user either way, is an InputError; a merge that
// leaves no enabled admin is ErrLastAdmin. It returns the original as it is now.
func (s *Service) RelinkSSO(ctx context.Context, original, duplicate int64) (Account, error) {
	if original == duplicate {
		return Account{}, &InputError{Msg: "a user can't be merged into itself"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("relink sso user: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	orig, err := scanAccount(tx.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM users WHERE id = ?`, original))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrUserNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("relink sso user: %w", err)
	}
	var dup struct {
		username, issuer, subject, display, email string
		role                                      Role
	}
	err = tx.QueryRowContext(ctx, `SELECT username, oidc_issuer, oidc_subject, display_name, email, role FROM users WHERE id = ?`,
		duplicate).Scan(&dup.username, &dup.issuer, &dup.subject, &dup.display, &dup.email, &dup.role)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, &InputError{Msg: "the duplicate user doesn't exist"}
	}
	if err != nil {
		return Account{}, fmt.Errorf("relink sso user: %w", err)
	}
	if !orig.SSO || dup.subject == "" {
		return Account{}, &InputError{Msg: "only SSO users can be re-linked; both must be SSO users"}
	}
	var owns []string
	for _, o := range duplicateOwns {
		var n int64
		if err := tx.QueryRowContext(ctx, o.query, duplicate).Scan(&n); err != nil {
			return Account{}, fmt.Errorf("relink sso user: count %s: %w", o.name, err)
		}
		if n > 0 {
			owns = append(owns, o.name)
		}
	}
	if len(owns) > 0 {
		return Account{}, &RelinkRefusedError{Owns: owns}
	}
	// The duplicate goes first: issuer and subject are unique.
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, duplicate); err != nil {
		return Account{}, fmt.Errorf("relink sso user: delete duplicate: %w", err)
	}
	a, err := scanAccount(tx.QueryRowContext(ctx, `
		UPDATE users SET oidc_issuer = ?, oidc_subject = ?, display_name = ?, email = ?, role = ?, updated_at = ?
		WHERE id = ?
		RETURNING `+accountColumns,
		dup.issuer, dup.subject, dup.display, dup.email, dup.role, s.now().Unix(), original))
	if err != nil {
		return Account{}, fmt.Errorf("relink sso user: update original: %w", err)
	}
	var admins bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE role = 'admin' AND NOT disabled)`).Scan(&admins); err != nil {
		return Account{}, fmt.Errorf("relink sso user: %w", err)
	}
	if !admins {
		return Account{}, ErrLastAdmin
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("relink sso user: %w", err)
	}
	if orig.Role != a.Role {
		s.log.Warn("re-linked sso user took the provider's role", "username", a.Username, "from", orig.Role, "to", a.Role)
	}
	s.log.Info("sso user re-linked", "username", a.Username, "duplicate", dup.username, "subject", dup.subject)
	return a, nil
}
