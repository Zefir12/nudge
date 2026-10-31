// Package deployments keeps deployments: a named stack in an environment that CI deploys
// through, with members (who may do what) and deploy tokens of its own. Every check of
// what a user may do with a deployment goes through Authorize, so new kinds of access
// (teams, environment roles) change one place.
package deployments

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxDescription = 500
	// MaxVariables bounds the variables a deployment lets deploys set.
	MaxVariables = 20
	// maxMembers bounds the members of one deployment.
	maxMembers = 100
	// maxDeployments bounds the deployments of all users together.
	maxDeployments = 1000
)

var (
	// ErrNotFound means there is no such deployment, or the actor may not see it.
	ErrNotFound = errors.New("deployment not found")
	// ErrForbidden means the actor sees the deployment but may not do that.
	ErrForbidden = errors.New("your role on this deployment doesn't allow that")
	// ErrTaken means another deployment has that name.
	ErrTaken = errors.New("a deployment with that name already exists")
	// ErrUnknownUser means no user has that username.
	ErrUnknownUser = errors.New("no user with that username")
	// ErrLastOwner means the change would leave the deployment without an owner.
	ErrLastOwner = errors.New("a deployment needs at least one owner")
	// ErrTooMany means a bound (members, tokens) is reached.
	ErrTooMany = errors.New("limit reached")
)

// InputError is a request that can't be valid; its message is safe to show.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }

// Role is what a member may do; each role may do everything the ones below it may.
type Role string

// The roles, lowest first: viewers read, deployers deploy, maintainers also edit and
// manage tokens, owners also manage members and delete.
const (
	RoleViewer     Role = "viewer"
	RoleDeployer   Role = "deployer"
	RoleMaintainer Role = "maintainer"
	RoleOwner      Role = "owner"
)

var roleRank = map[Role]int{RoleViewer: 1, RoleDeployer: 2, RoleMaintainer: 3, RoleOwner: 4}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return roleRank[r] > 0 }

// Action is something done with a deployment.
type Action string

const (
	// ActionView reads the deployment, its runs and its members.
	ActionView Action = "view"
	// ActionDeploy starts a run.
	ActionDeploy Action = "deploy"
	// ActionEdit changes the description and the variables deploys may set.
	ActionEdit Action = "edit"
	// ActionManageTokens creates and revokes deploy tokens.
	ActionManageTokens Action = "manage_tokens"
	// ActionManageMembers adds, changes and removes members.
	ActionManageMembers Action = "manage_members"
	// ActionDelete removes the deployment.
	ActionDelete Action = "delete"
)

// minRole is the lowest role allowed each action.
var minRole = map[Action]Role{
	ActionView:          RoleViewer,
	ActionDeploy:        RoleDeployer,
	ActionEdit:          RoleMaintainer,
	ActionManageTokens:  RoleMaintainer,
	ActionManageMembers: RoleOwner,
	ActionDelete:        RoleOwner,
}

// Allows reports whether role r may do a.
func (r Role) Allows(a Action) bool {
	least, ok := minRole[a]
	return ok && roleRank[r] >= roleRank[least]
}

// Actor is the user asking. Admins may do everything with every deployment.
type Actor struct {
	UserID   int64
	Username string
	Admin    bool
}

// Deployment is a stack CI deploys through.
type Deployment struct {
	ID          int64
	Name        string
	Environment int64
	Stack       string
	Description string
	// Variables are the .env variables a deploy may set.
	Variables []string
	// Health says when a run succeeded.
	Health    Health
	CreatedAt time.Time
	UpdatedAt time.Time
	// Role is the actor's role; empty for an admin who isn't a member.
	Role Role
}

// Input is a new deployment.
type Input struct {
	Name        string
	Environment int64
	Stack       string
	Description string
	Variables   []string
	Health      Health
}

// Update is a change of a deployment; nil fields stay.
type Update struct {
	Description *string
	Variables   []string
	// SetVariables says whether Variables replaces the list (an empty list clears it).
	SetVariables bool
	Health       *Health
}

// Member is a user's role on a deployment.
type Member struct {
	UserID   int64
	Username string
	Role     Role
}

// Clock is the time source; tests skip the waits.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options are the Service's dependencies.
type Options struct {
	// Context ends the runs: at shutdown the running deploys see it end.
	Context context.Context
	DB      *sql.DB
	Log     *slog.Logger
	Clock   Clock
}

// Service keeps deployments and runs their deploys.
type Service struct {
	db     *sql.DB
	log    *slog.Logger
	clock  Clock
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	// slots are the deployments with a run under way, so bounded by the deployments
	// table; a slot goes when its runs end.
	slots map[int64]*slot
	wg    sync.WaitGroup
}

// New returns a Service.
func New(opts Options) *Service {
	parent := opts.Context
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{db: opts.DB, log: opts.Log, clock: opts.Clock, ctx: ctx, cancel: cancel, slots: map[int64]*slot{}}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.clock == nil {
		s.clock = realClock{}
	}
	return s
}

var (
	namePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	stackPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	variablePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
)

// ValidName reports whether name can name a deployment: lowercase letters, digits and
// dashes, up to 63, as it appears in URLs.
func ValidName(name string) bool { return namePattern.MatchString(name) }

func validDescription(d string) error {
	if len(d) > maxDescription || !utf8.ValidString(d) {
		return &InputError{Msg: fmt.Sprintf("description must be valid text of at most %d bytes", maxDescription)}
	}
	return nil
}

// cleanVariables checks the names and drops repeats, keeping the order.
func cleanVariables(vars []string) ([]string, error) {
	if len(vars) > MaxVariables {
		return nil, &InputError{Msg: fmt.Sprintf("at most %d variables", MaxVariables)}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range vars {
		if !variablePattern.MatchString(v) {
			return nil, &InputError{Msg: fmt.Sprintf("variable %q: use uppercase letters, digits and _, not starting with a digit", v)}
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out, nil
}

func (in *Input) validate() error {
	if !ValidName(in.Name) {
		return &InputError{Msg: "name must be 1 to 63 lowercase letters, digits and dashes, starting with a letter or digit"}
	}
	if in.Environment <= 0 {
		return &InputError{Msg: "environment is required"}
	}
	if !stackPattern.MatchString(in.Stack) {
		return &InputError{Msg: "stack must name a stack"}
	}
	if err := validDescription(in.Description); err != nil {
		return err
	}
	vars, err := cleanVariables(in.Variables)
	if err != nil {
		return err
	}
	in.Variables = vars
	in.Health.HTTPChecks = slices.Clone(in.Health.HTTPChecks)
	return in.Health.clean()
}

func (s *Service) now() int64 { return s.clock.Now().UnixMilli() }

// Create adds a deployment with the actor as its owner.
func (s *Service) Create(ctx context.Context, actor Actor, in Input) (Deployment, error) {
	if err := in.validate(); err != nil {
		return Deployment{}, err
	}
	vars, err := json.Marshal(in.Variables)
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	health, err := json.Marshal(in.Health)
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments`).Scan(&count); err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	if count >= maxDeployments {
		return Deployment{}, fmt.Errorf("%w: at most %d deployments", ErrTooMany, maxDeployments)
	}
	// Names are unique across every user, so a taken one is reported even when the
	// actor can't see that deployment.
	res, err := tx.ExecContext(ctx, `INSERT INTO deployments (name, environment_id, stack, description, variables, health, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (name) DO NOTHING`,
		in.Name, in.Environment, in.Stack, in.Description, string(vars), string(health), now, now)
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		if err != nil {
			return Deployment{}, fmt.Errorf("create deployment: %w", err)
		}
		return Deployment{}, ErrTaken
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployment_members (deployment_id, user_id, role) VALUES (?, ?, ?)`, id, actor.UserID, string(RoleOwner)); err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	return s.Authorize(ctx, actor, in.Name, ActionView)
}

const columns = `d.id, d.name, d.environment_id, d.stack, d.description, d.variables, d.health, d.created_at, d.updated_at, COALESCE(m.role, '')`

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Deployment, error) {
	var d Deployment
	var vars, health string
	var created, updated int64
	if err := row.Scan(&d.ID, &d.Name, &d.Environment, &d.Stack, &d.Description, &vars, &health, &created, &updated, &d.Role); err != nil {
		return Deployment{}, err
	}
	if err := json.Unmarshal([]byte(vars), &d.Variables); err != nil {
		return Deployment{}, fmt.Errorf("deployment %s variables: %w", d.Name, err)
	}
	if err := json.Unmarshal([]byte(health), &d.Health); err != nil {
		return Deployment{}, fmt.Errorf("deployment %s health: %w", d.Name, err)
	}
	d.CreatedAt = time.UnixMilli(created).UTC()
	d.UpdatedAt = time.UnixMilli(updated).UTC()
	return d, nil
}

// Authorize returns the deployment named name if actor may do a with it. A deployment
// the actor may not even view is ErrNotFound, so its name doesn't leak; one it may view
// but not do a with is ErrForbidden.
func (s *Service) Authorize(ctx context.Context, actor Actor, name string, a Action) (Deployment, error) {
	if !ValidName(name) {
		return Deployment{}, ErrNotFound
	}
	d, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM deployments d
		LEFT JOIN deployment_members m ON m.deployment_id = d.id AND m.user_id = ?
		WHERE d.name = ?`, actor.UserID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("get deployment: %w", err)
	}
	switch {
	case actor.Admin, d.Role.Allows(a):
		return d, nil
	case d.Role.Allows(ActionView):
		return Deployment{}, ErrForbidden
	default:
		return Deployment{}, ErrNotFound
	}
}

// List returns the deployments actor may view, by name; with env > 0 only that
// environment's.
func (s *Service) List(ctx context.Context, actor Actor, env int64) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM deployments d
		LEFT JOIN deployment_members m ON m.deployment_id = d.id AND m.user_id = ?
		WHERE (? OR m.role IS NOT NULL) AND (? = 0 OR d.environment_id = ?)
		ORDER BY d.name`, actor.UserID, actor.Admin, env, env)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Deployment{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("list deployments: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	return out, nil
}

// Change applies u to the deployment named name.
func (s *Service) Change(ctx context.Context, actor Actor, name string, u Update) (Deployment, error) {
	d, err := s.Authorize(ctx, actor, name, ActionEdit)
	if err != nil {
		return Deployment{}, err
	}
	if u.Description != nil {
		if err := validDescription(*u.Description); err != nil {
			return Deployment{}, err
		}
		d.Description = *u.Description
	}
	if u.SetVariables {
		vars, err := cleanVariables(u.Variables)
		if err != nil {
			return Deployment{}, err
		}
		d.Variables = vars
	}
	if u.Health != nil {
		h := *u.Health
		h.HTTPChecks = slices.Clone(h.HTTPChecks)
		if err := h.clean(); err != nil {
			return Deployment{}, err
		}
		d.Health = h
	}
	vars, err := json.Marshal(d.Variables)
	if err != nil {
		return Deployment{}, fmt.Errorf("change deployment: %w", err)
	}
	health, err := json.Marshal(d.Health)
	if err != nil {
		return Deployment{}, fmt.Errorf("change deployment: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE deployments SET description = ?, variables = ?, health = ?, updated_at = ? WHERE id = ?`,
		d.Description, string(vars), string(health), s.now(), d.ID); err != nil {
		return Deployment{}, fmt.Errorf("change deployment: %w", err)
	}
	return s.Authorize(ctx, actor, name, ActionView)
}

// Delete removes the deployment named name with its members, tokens and runs. A run
// under way finishes, but its result isn't kept.
func (s *Service) Delete(ctx context.Context, actor Actor, name string) error {
	d, err := s.Authorize(ctx, actor, name, ActionDelete)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM deployments WHERE id = ?`, d.ID); err != nil {
		return fmt.Errorf("delete deployment: %w", err)
	}
	return nil
}

// Members returns the deployment's members by username.
func (s *Service) Members(ctx context.Context, actor Actor, name string) ([]Member, error) {
	d, err := s.Authorize(ctx, actor, name, ActionView)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT u.id, u.username, m.role FROM deployment_members m
		JOIN users u ON u.id = m.user_id WHERE m.deployment_id = ? ORDER BY u.username`, d.ID)
	if err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Username, &m.Role); err != nil {
			return nil, fmt.Errorf("list members: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list members: %w", err)
	}
	return out, nil
}

// SetMember gives the user named username role on the deployment, adding them if they
// aren't a member.
func (s *Service) SetMember(ctx context.Context, actor Actor, name, username string, role Role) (Member, error) {
	if !role.Valid() {
		return Member{}, &InputError{Msg: "role must be viewer, deployer, maintainer or owner"}
	}
	d, err := s.Authorize(ctx, actor, name, ActionManageMembers)
	if err != nil {
		return Member{}, err
	}
	m := Member{Username: strings.TrimSpace(username), Role: role}
	err = s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, m.Username).Scan(&m.UserID)
	if errors.Is(err, sql.ErrNoRows) {
		return Member{}, ErrUnknownUser
	}
	if err != nil {
		return Member{}, fmt.Errorf("set member: %w", err)
	}
	return m, s.changeMembers(ctx, d.ID, func(tx *sql.Tx) error {
		var exists bool
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM deployment_members WHERE deployment_id = ? AND user_id = ?),
			(SELECT COUNT(*) FROM deployment_members WHERE deployment_id = ?)`, d.ID, m.UserID, d.ID).Scan(&exists, &count); err != nil {
			return err
		}
		if !exists && count >= maxMembers {
			return fmt.Errorf("%w: at most %d members", ErrTooMany, maxMembers)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO deployment_members (deployment_id, user_id, role) VALUES (?, ?, ?)
			ON CONFLICT (deployment_id, user_id) DO UPDATE SET role = excluded.role`, d.ID, m.UserID, string(role))
		return err
	})
}

// RemoveMember takes the user's role on the deployment away.
func (s *Service) RemoveMember(ctx context.Context, actor Actor, name string, userID int64) error {
	d, err := s.Authorize(ctx, actor, name, ActionManageMembers)
	if err != nil {
		return err
	}
	return s.changeMembers(ctx, d.ID, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM deployment_members WHERE deployment_id = ? AND user_id = ?`, d.ID, userID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrNotFound
		}
		return err
	})
}

// changeMembers runs change in a transaction that fails if it leaves the deployment
// without an owner.
func (s *Service) changeMembers(ctx context.Context, id int64, change func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("change members: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := change(tx); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooMany) {
			return err
		}
		return fmt.Errorf("change members: %w", err)
	}
	var owners int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_members WHERE deployment_id = ? AND role = 'owner'`, id).Scan(&owners); err != nil {
		return fmt.Errorf("change members: %w", err)
	}
	if owners == 0 {
		return ErrLastOwner
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change members: %w", err)
	}
	return nil
}
