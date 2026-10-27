package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrUserNotFound means no user has that ID.
	ErrUserNotFound = errors.New("user not found")
	// ErrUserExists means the username is taken.
	ErrUserExists = errors.New("a user with that name already exists")
	// ErrLastAdmin means the change would leave no enabled admin.
	ErrLastAdmin = errors.New("the last enabled admin can't be demoted, disabled or deleted")
	// ErrBreakGlass means the change would remove the break-glass admin while password
	// login is off for everyone else.
	ErrBreakGlass = errors.New("the break-glass admin can't be demoted, disabled or deleted while password login is off")
)

// Account is a user as the user management lists it.
type Account struct {
	User
	Disabled    bool
	DisplayName string
	Email       string
}

// Change edits a user; nil fields stay as they are.
type Change struct {
	Role     *Role
	Disabled *bool
	// Password resets a local user's password.
	Password *string
}

// otherAdmins is true while an enabled admin other than the ? user exists: the guard
// every change that can remove an admin runs in its own statement, so concurrent changes
// can't remove the last two together.
const otherAdmins = `EXISTS (SELECT 1 FROM users WHERE role = 'admin' AND NOT disabled AND id <> ?)`

// otherAdminsN is otherAdmins for statements with numbered parameters, the user in ?5.
const otherAdminsN = `EXISTS (SELECT 1 FROM users WHERE role = 'admin' AND NOT disabled AND id <> ?5)`

const accountColumns = `id, username, created_at, role, oidc_issuer <> '', disabled, display_name, email`

func scanAccount(row interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var created int64
	if err := row.Scan(&a.ID, &a.Username, &created, &a.Role, &a.SSO, &a.Disabled, &a.DisplayName, &a.Email); err != nil {
		return Account{}, err
	}
	a.CreatedAt = time.Unix(created, 0)
	return a, nil
}

// ListUsers returns every user by name.
func (s *Service) ListUsers(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountColumns+` FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return out, nil
}

// GetUser returns the user with id.
func (s *Service) GetUser(ctx context.Context, id int64) (Account, error) {
	a, err := scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrUserNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("get user: %w", err)
	}
	return a, nil
}

// CreateUser creates a local user with a password.
func (s *Service) CreateUser(ctx context.Context, username, password string, role Role) (Account, error) {
	if err := validUsername(username); err != nil {
		return Account{}, err
	}
	if err := validPassword(password); err != nil {
		return Account{}, err
	}
	if !role.Valid() {
		return Account{}, &InputError{Msg: `role must be "admin" or "user"`}
	}
	hash, err := s.hash(ctx, password)
	if err != nil {
		return Account{}, err
	}
	now := s.now().Unix()
	a, err := scanAccount(s.db.QueryRowContext(ctx, `
		INSERT INTO users (username, password_hash, created_at, updated_at, role) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
		RETURNING `+accountColumns, username, hash, now, now, role))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrUserExists
	}
	if err != nil {
		return Account{}, fmt.Errorf("create user: %w", err)
	}
	return a, nil
}

// UpdateUser applies c to the user with id. An SSO user's role is the provider's and
// they have no password, so only Disabled applies to them. A change that leaves no
// enabled admin is ErrLastAdmin. Disabling a user or resetting their password ends their
// sessions; a disabled user's API tokens stop working until they are enabled again.
func (s *Service) UpdateUser(ctx context.Context, id int64, c Change) (Account, error) {
	cur, err := s.GetUser(ctx, id)
	if err != nil {
		return Account{}, err
	}
	if c.Role != nil && !c.Role.Valid() {
		return Account{}, &InputError{Msg: `role must be "admin" or "user"`}
	}
	if cur.SSO && c.Role != nil && *c.Role != cur.Role {
		return Account{}, &InputError{Msg: "an SSO user's role comes from the provider"}
	}
	if cur.SSO && c.Password != nil {
		return Account{}, &InputError{Msg: "an SSO user has no password in nudge"}
	}
	if s.breakGlass(cur) && (c.Role != nil && *c.Role != RoleAdmin || c.Disabled != nil && *c.Disabled) {
		return Account{}, ErrBreakGlass
	}
	hash := ""
	if c.Password != nil {
		if err := validPassword(*c.Password); err != nil {
			return Account{}, err
		}
		if hash, err = s.hash(ctx, *c.Password); err != nil {
			return Account{}, err
		}
	}
	// Only the given fields are written, so concurrent changes of other fields stay; the
	// last-admin guard reads the row as it is now, in the same statement.
	var role, disabled any
	if c.Role != nil {
		role = string(*c.Role)
	}
	if c.Disabled != nil {
		disabled = *c.Disabled
	}
	a, err := scanAccount(s.db.QueryRowContext(ctx, `
		UPDATE users SET role = COALESCE(?1, role), disabled = COALESCE(?2, disabled),
			password_hash = CASE WHEN ?3 = '' THEN password_hash ELSE ?3 END, updated_at = ?4
		WHERE id = ?5 AND ((COALESCE(?1, role) = 'admin' AND NOT COALESCE(?2, disabled)) OR `+otherAdminsN+`)
		RETURNING `+accountColumns,
		role, disabled, hash, s.now().Unix(), id))
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.GetUser(ctx, id); err != nil {
			return Account{}, err
		}
		return Account{}, ErrLastAdmin
	}
	if err != nil {
		return Account{}, fmt.Errorf("update user: %w", err)
	}
	if a.Disabled || c.Password != nil {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return Account{}, fmt.Errorf("end sessions: %w", err)
		}
	}
	return a, nil
}

// DeleteUser deletes the user with id, their sessions, API tokens and everything else
// that belongs to them (the foreign keys cascade). The last enabled admin is
// ErrLastAdmin.
func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	cur, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if s.breakGlass(cur) {
		return ErrBreakGlass
	}
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM users WHERE id = ? AND (role <> 'admin' OR disabled OR `+otherAdmins+`)`, id, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if n == 0 {
		if _, err := s.GetUser(ctx, id); err != nil {
			return err
		}
		return ErrLastAdmin
	}
	return nil
}

// breakGlass reports whether a is the local AdminUsername while password login is off for
// everyone else (AdminPasswordOnly): the only way in when the SSO provider fails.
func (s *Service) breakGlass(a Account) bool {
	return s.adminOnly && !a.SSO && strings.EqualFold(a.Username, AdminUsername)
}
