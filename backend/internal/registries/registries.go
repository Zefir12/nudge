// Package registries stores the credentials nudge pulls images with. Passwords are
// encrypted with a secretbox.Box (they must be sent to the daemon, so they can't be
// hashed) and never leave the package except to authenticate a pull.
package registries

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.zefirlabs.net/zefir/nudge/backend/internal/secretbox"
)

// DockerHub is the address of Docker Hub, the registry of images without a host.
const DockerHub = "docker.io"

// passwordPurpose binds sealed passwords to this use of the key.
const passwordPurpose = "registry password"

const (
	maxName     = 64
	maxUsername = 255
	maxPassword = 4096
)

var (
	// ErrNotFound means there is no registry with that id.
	ErrNotFound = errors.New("registry not found")
	// ErrTaken means another registry has that name or address.
	ErrTaken = errors.New("a registry with that name or address already exists")
)

// InputError is a validation error whose message can be shown to the user.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Registry is a registry's stored credentials, without the password.
type Registry struct {
	ID        int64
	Name      string
	Address   string
	Username  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Input creates or updates a registry. A nil Password keeps the current one on update.
type Input struct {
	Name     string
	Address  string
	Username string
	Password *string
}

// Auth is what a pull from Address authenticates with.
type Auth struct {
	Address  string
	Username string
	Password string
}

// Options are the Service's dependencies.
type Options struct {
	DB  *sql.DB
	Box *secretbox.Box
	// Now is the clock (time.Now by default).
	Now func() time.Time
}

// Service manages registry credentials.
type Service struct {
	db  *sql.DB
	box *secretbox.Box
	now func() time.Time
}

// New returns a Service.
func New(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{db: opts.DB, box: opts.Box, now: now}
}

const selectRegistry = `SELECT id, name, address, username, created_at, updated_at FROM registries`

// List returns every registry by name.
func (s *Service) List(ctx context.Context) ([]Registry, error) {
	rows, err := s.db.QueryContext(ctx, selectRegistry+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var list []Registry
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list registries: %w", err)
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list registries: %w", err)
	}
	return list, nil
}

// Get returns the registry with id.
func (s *Service) Get(ctx context.Context, id int64) (Registry, error) {
	r, err := scan(s.db.QueryRowContext(ctx, selectRegistry+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Registry{}, ErrNotFound
	}
	if err != nil {
		return Registry{}, fmt.Errorf("get registry: %w", err)
	}
	return r, nil
}

// Create stores a registry; the password is required.
func (s *Service) Create(ctx context.Context, in Input) (Registry, error) {
	address, err := validate(&in, true)
	if err != nil {
		return Registry{}, err
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO registries (name, address, username, password_enc, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		in.Name, address, in.Username, s.box.Seal([]byte(*in.Password), passwordPurpose), now, now)
	if err != nil {
		return Registry{}, fmt.Errorf("create registry: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return Registry{}, fmt.Errorf("create registry: %w", err)
		}
		return Registry{}, ErrTaken
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Registry{}, fmt.Errorf("create registry: %w", err)
	}
	return s.Get(ctx, id)
}

// Update replaces a registry's fields, keeping the password if in.Password is nil.
func (s *Service) Update(ctx context.Context, id int64, in Input) (Registry, error) {
	address, err := validate(&in, false)
	if err != nil {
		return Registry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Registry{}, fmt.Errorf("update registry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM registries WHERE id = ?)`, id).Scan(&exists); err != nil {
		return Registry{}, fmt.Errorf("update registry: %w", err)
	}
	if !exists {
		return Registry{}, ErrNotFound
	}
	var taken bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM registries WHERE id != ? AND (name = ? OR address = ?))`,
		id, in.Name, address).Scan(&taken); err != nil {
		return Registry{}, fmt.Errorf("update registry: %w", err)
	}
	if taken {
		return Registry{}, ErrTaken
	}
	now := s.now().Unix()
	if in.Password != nil {
		_, err = tx.ExecContext(ctx, `UPDATE registries SET name = ?, address = ?, username = ?, password_enc = ?, updated_at = ? WHERE id = ?`,
			in.Name, address, in.Username, s.box.Seal([]byte(*in.Password), passwordPurpose), now, id)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE registries SET name = ?, address = ?, username = ?, updated_at = ? WHERE id = ?`,
			in.Name, address, in.Username, now, id)
	}
	if err != nil {
		return Registry{}, fmt.Errorf("update registry: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Registry{}, fmt.Errorf("update registry: %w", err)
	}
	return s.Get(ctx, id)
}

// Delete removes the registry with id.
func (s *Service) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM registries WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete registry: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete registry: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// AuthFor returns the credentials for the registry an image reference names, and false
// if none are stored.
func (s *Service) AuthFor(ctx context.Context, ref string) (Auth, bool, error) {
	address := HostOf(ref)
	var a Auth
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT address, username, password_enc FROM registries WHERE address = ?`, address).
		Scan(&a.Address, &a.Username, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return Auth{}, false, nil
	}
	if err != nil {
		return Auth{}, false, fmt.Errorf("registry credentials for %s: %w", address, err)
	}
	password, err := s.box.Open(sealed, passwordPurpose)
	if err != nil {
		return Auth{}, false, fmt.Errorf("registry credentials for %s: %w", address, err)
	}
	a.Password = string(password)
	return a, true, nil
}

// HostOf returns the registry address of an image reference: its first path component
// if that looks like a host (has a dot or port, or is localhost), else Docker Hub.
func HostOf(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	first, _, ok := strings.Cut(name, "/")
	if !ok || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		return DockerHub
	}
	return canonicalHost(strings.ToLower(first))
}

// address matches a registry host with an optional port.
var address = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?(:[0-9]{1,5})?$`)

// NormalizeAddress turns what users paste (https://ghcr.io/, index.docker.io/v1/) into
// the host that image references name.
func NormalizeAddress(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	s, _, _ = strings.Cut(s, "/")
	if !address.MatchString(s) {
		return "", &InputError{Msg: "address must be a registry host such as ghcr.io or registry.example.com:5000"}
	}
	return canonicalHost(s), nil
}

func canonicalHost(host string) string {
	switch host {
	case "index.docker.io", "registry-1.docker.io", "registry.hub.docker.com":
		return DockerHub
	}
	return host
}

func validate(in *Input, create bool) (string, error) {
	if err := validText("name", in.Name, maxName); err != nil {
		return "", err
	}
	if err := validText("username", in.Username, maxUsername); err != nil {
		return "", err
	}
	if create && in.Password == nil {
		return "", &InputError{Msg: "password is required"}
	}
	if in.Password != nil && (*in.Password == "" || len(*in.Password) > maxPassword) {
		return "", &InputError{Msg: fmt.Sprintf("password must be 1 to %d bytes", maxPassword)}
	}
	return NormalizeAddress(in.Address)
}

func validText(field, s string, maxLen int) error {
	if s == "" || len(s) > maxLen {
		return &InputError{Msg: fmt.Sprintf("%s must be 1 to %d bytes", field, maxLen)}
	}
	if !utf8.ValidString(s) || strings.TrimSpace(s) != s || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return &InputError{Msg: field + " must be valid UTF-8 without control characters or surrounding spaces"}
	}
	return nil
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Registry, error) {
	var r Registry
	var created, updated int64
	if err := row.Scan(&r.ID, &r.Name, &r.Address, &r.Username, &created, &updated); err != nil {
		return Registry{}, err
	}
	r.CreatedAt = time.Unix(created, 0).UTC()
	r.UpdatedAt = time.Unix(updated, 0).UTC()
	return r, nil
}
