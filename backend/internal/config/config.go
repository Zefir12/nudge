// Package config loads nudge's settings from flags, with NUDGE_* environment variables
// as fallbacks and defaults that work in the published image without any setup.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// hostPort is a host name or IP literal with an optional port. The public URL goes into
// the docker run command users paste into a shell, so nothing else is allowed.
var hostPort = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$|^\[[0-9A-Fa-f:.]+\](:[0-9]{1,5})?$`)

// Server is the configuration of `nudge serve`.
type Server struct {
	// Addr is the listen address of the API and web UI.
	Addr string
	// DataDir holds the database and stack files. The image mounts a volume here.
	DataDir string
	// StacksDir holds the compose stacks; empty flag means <DataDir>/stacks. Relative
	// bind mounts need it mounted at the same path on the host and in the container.
	StacksDir string
	// Compose is the docker-compose binary that deploys stacks.
	Compose string
	// LogLevel is debug, info, warn or error.
	LogLevel slog.Level
	// AdminPassword, if set, creates the user "admin" at start when no user exists.
	// Environment only (NUDGE_ADMIN_PASSWORD): a flag would show in the process list.
	AdminPassword string
	// DockerHost is the local environment's Docker API, unix:// or tcp://.
	DockerHost string
	// TLSCert and TLSKey are PEM files; when set the server speaks HTTPS only.
	TLSCert string
	TLSKey  string
	// PublicURL is the URL browsers and agents reach the server at, scheme://host[:port]
	// without a trailing slash; empty means guess it from each request.
	PublicURL string
	// TrustedProxies are the reverse proxies whose X-Forwarded-For and X-Forwarded-Proto
	// headers are believed; empty means none.
	TrustedProxies []netip.Prefix
	// AuditRetentionDays is how long audit log entries are kept.
	AuditRetentionDays int
	// UpdateCheckInterval is how often images are checked for updates; zero turns the
	// scheduled check off.
	UpdateCheckInterval time.Duration
	// UsageInterval is how often disk usage (volume sizes) is measured; zero turns the
	// scheduled measurement off.
	UsageInterval time.Duration
	// OIDC is the single sign-on provider; zero when SSO is off.
	OIDC OIDC
	// NoPasswordLogin turns password login off for everyone but the local break-glass
	// user "admin" (NUDGE_PASSWORD_LOGIN=false); it needs OIDC.
	NoPasswordLogin bool
	// Tenant hardens users' containers with the host's setup; zero adds nothing.
	Tenant Tenant
	// UserRegistries are the registry hosts users' pulls may reach, "*" for any; nil
	// means the API's default (Docker Hub and the big public registries).
	UserRegistries []string
	// Backup is the scheduled backup; a zero Interval turns it off.
	Backup Backup
}

// Backup configures scheduled backups to <data>/backups.
type Backup struct {
	Interval time.Duration
	// Keep is how many scheduled backups are kept.
	Keep int
	// PassphraseFile holds the passphrase the backups are encrypted with.
	PassphraseFile string
}

// Tenant is the hardening of users' containers that needs the host set up for it
// (docs/config/server.md, "Hosting other users").
type Tenant struct {
	// CgroupParent is a user's cgroup parent, {id} standing for the user's ID, e.g.
	// nudge-u{id}.slice.
	CgroupParent string
	// Runtime is the OCI runtime of users' containers, e.g. runsc (gVisor).
	Runtime string
	// StorageSize caps a user's container's writable layer, in bytes.
	StorageSize int64
	// DeniedLabels are the label prefixes (lower case) users may not set on their
	// containers, so a user can't steer label-driven tools on the host (reverse proxies,
	// updaters). Empty denies none.
	DeniedLabels []string
}

// DefaultDeniedLabels is the default of -tenant-denied-labels: the labels of common
// reverse proxies (Traefik, caddy-docker-proxy), updaters (Watchtower, What's Up Docker,
// Diun) and the Ofelia job scheduler.
const DefaultDeniedLabels = "traefik.,caddy,com.centurylinklabs.watchtower.,ofelia.,wud.,diun."

var (
	// cgroupParent is a cgroup parent template: a systemd slice name or a cgroupfs path,
	// with {id} for the user's ID.
	cgroupParent = regexp.MustCompile(`^[A-Za-z0-9_.@/{}-]+$`)
	// runtimeName is a runtime as the daemon lists it.
	runtimeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	// sizePattern is a size in bytes with an optional binary unit: 512M, 10G, 10GiB.
	sizePattern = regexp.MustCompile(`^([0-9]+)([KMGT]?)(?:I?B)?$`)
)

// OIDC is the OpenID Connect provider users sign in with.
type OIDC struct {
	// Issuer is the provider's issuer URL (https://), where discovery starts.
	Issuer   string
	ClientID string
	// ClientSecret is environment only (NUDGE_OIDC_CLIENT_SECRET), like the admin password.
	ClientSecret string
	// Name labels the login button.
	Name string
	// AdminRole is the value of the ID token's roles claim that makes a user an admin.
	AdminRole string
	// UserRole is the value of the roles claim that makes a user a (non-admin) user.
	UserRole string
	// RequireMFA refuses logins whose amr claim lacks "mfa".
	RequireMFA bool
	// SessionTTL caps the sessions of SSO logins, as the provider can't end them.
	SessionTTL time.Duration
}

// Enabled reports whether SSO is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" }

// TLS reports whether the server serves HTTPS itself.
func (c Server) TLS() bool { return c.TLSCert != "" }

// LoadServer parses args (without the program and subcommand name). A flag wins over
// its environment variable, which wins over the default. Usage and errors go to out.
func LoadServer(args []string, getenv func(string) string, out io.Writer) (Server, error) {
	var c Server
	fs := flag.NewFlagSet("nudge serve", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&c.Addr, "addr", envOr(getenv, "NUDGE_ADDR", ":8080"), "listen address (NUDGE_ADDR)")
	fs.StringVar(&c.DataDir, "data", envOr(getenv, "NUDGE_DATA", "/data"), "data directory (NUDGE_DATA)")
	fs.StringVar(&c.StacksDir, "stacks-dir", getenv("NUDGE_STACKS_DIR"), "compose stacks directory, mounted at the same path on the host (NUDGE_STACKS_DIR; default <data>/stacks)")
	fs.StringVar(&c.Compose, "compose", envOr(getenv, "NUDGE_COMPOSE", "/usr/local/bin/docker-compose"), "docker-compose binary that deploys stacks (NUDGE_COMPOSE)")
	fs.StringVar(&c.DockerHost, "docker-host", envOr(getenv, "DOCKER_HOST", "unix:///var/run/docker.sock"), "Docker API of the local environment, unix:// or tcp:// (DOCKER_HOST)")
	fs.StringVar(&c.TLSCert, "tls-cert", getenv("NUDGE_TLS_CERT"), "PEM certificate (chain) file; serve HTTPS with -tls-key (NUDGE_TLS_CERT)")
	fs.StringVar(&c.TLSKey, "tls-key", getenv("NUDGE_TLS_KEY"), "PEM private key file for -tls-cert (NUDGE_TLS_KEY)")
	fs.StringVar(&c.PublicURL, "public-url", getenv("NUDGE_PUBLIC_URL"), "URL browsers and agents reach the server at, e.g. https://nudge.example.net (NUDGE_PUBLIC_URL)")
	proxyList := fs.String("trusted-proxies", getenv("NUDGE_TRUSTED_PROXIES"), "comma-separated CIDRs or IPs of reverse proxies whose X-Forwarded-For and X-Forwarded-Proto are trusted (NUDGE_TRUSTED_PROXIES)")
	retention, err := strconv.Atoi(envOr(getenv, "NUDGE_AUDIT_RETENTION_DAYS", "90"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_AUDIT_RETENTION_DAYS %q: want a number of days", getenv("NUDGE_AUDIT_RETENTION_DAYS"))
	}
	fs.IntVar(&c.AuditRetentionDays, "audit-retention-days", retention, "days to keep audit log entries (NUDGE_AUDIT_RETENTION_DAYS)")
	interval, err := time.ParseDuration(envOr(getenv, "NUDGE_UPDATE_CHECK_INTERVAL", "6h"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_UPDATE_CHECK_INTERVAL %q: want a duration such as 6h or 30m", getenv("NUDGE_UPDATE_CHECK_INTERVAL"))
	}
	fs.DurationVar(&c.UpdateCheckInterval, "update-check-interval", interval, "how often to check images for updates, 0 for never (NUDGE_UPDATE_CHECK_INTERVAL)")
	usage, err := time.ParseDuration(envOr(getenv, "NUDGE_USAGE_INTERVAL", "15m"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_USAGE_INTERVAL %q: want a duration such as 15m or 1h", getenv("NUDGE_USAGE_INTERVAL"))
	}
	fs.DurationVar(&c.UsageInterval, "usage-interval", usage, "how often to measure volume sizes, 0 for never (NUDGE_USAGE_INTERVAL)")
	fs.StringVar(&c.OIDC.Issuer, "oidc-issuer", getenv("NUDGE_OIDC_ISSUER"), "OpenID Connect issuer URL; turns on single sign-on with the client ID and secret (NUDGE_OIDC_ISSUER)")
	fs.StringVar(&c.OIDC.ClientID, "oidc-client-id", getenv("NUDGE_OIDC_CLIENT_ID"), "OpenID Connect client ID (NUDGE_OIDC_CLIENT_ID)")
	fs.StringVar(&c.OIDC.Name, "oidc-name", envOr(getenv, "NUDGE_OIDC_NAME", "SSO"), "label of the single sign-on button (NUDGE_OIDC_NAME)")
	fs.StringVar(&c.OIDC.AdminRole, "oidc-admin-role", envOr(getenv, "NUDGE_OIDC_ADMIN_ROLE", "admin"), "value of the ID token's roles claim that makes a user an admin (NUDGE_OIDC_ADMIN_ROLE)")
	fs.StringVar(&c.OIDC.UserRole, "oidc-user-role", envOr(getenv, "NUDGE_OIDC_USER_ROLE", "user"), "value of the ID token's roles claim that makes a user a (non-admin) user; empty signs in admins only (NUDGE_OIDC_USER_ROLE)")
	requireMFA, err := strconv.ParseBool(envOr(getenv, "NUDGE_OIDC_REQUIRE_MFA", "false"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_OIDC_REQUIRE_MFA %q: want true or false", getenv("NUDGE_OIDC_REQUIRE_MFA"))
	}
	fs.BoolVar(&c.OIDC.RequireMFA, "oidc-require-mfa", requireMFA, "refuse single sign-on without a second factor (amr has no mfa) (NUDGE_OIDC_REQUIRE_MFA)")
	ssoTTL, err := time.ParseDuration(envOr(getenv, "NUDGE_OIDC_SESSION_TTL", "12h"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_OIDC_SESSION_TTL %q: want a duration such as 12h", getenv("NUDGE_OIDC_SESSION_TTL"))
	}
	fs.DurationVar(&c.OIDC.SessionTTL, "oidc-session-ttl", ssoTTL, "how long a single sign-on session lasts (NUDGE_OIDC_SESSION_TTL)")
	passwordLogin, err := strconv.ParseBool(envOr(getenv, "NUDGE_PASSWORD_LOGIN", "true"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_PASSWORD_LOGIN %q: want true or false", getenv("NUDGE_PASSWORD_LOGIN"))
	}
	fs.BoolVar(&passwordLogin, "password-login", passwordLogin, "allow password login; false leaves it only to the local admin user (NUDGE_PASSWORD_LOGIN)")
	fs.StringVar(&c.Tenant.CgroupParent, "tenant-cgroup-parent", getenv("NUDGE_TENANT_CGROUP_PARENT"), "cgroup parent of each user's containers, {id} for the user's ID, e.g. nudge-u{id}.slice (NUDGE_TENANT_CGROUP_PARENT)")
	fs.StringVar(&c.Tenant.Runtime, "tenant-runtime", getenv("NUDGE_TENANT_RUNTIME"), "OCI runtime of users' containers, e.g. runsc (NUDGE_TENANT_RUNTIME)")
	userRegistries := fs.String("user-registries", getenv("NUDGE_USER_REGISTRIES"), "comma-separated registry hosts users' pulls may reach, * for any (NUDGE_USER_REGISTRIES; default docker.io and the big public registries)")
	storageSize := fs.String("tenant-storage-size", getenv("NUDGE_TENANT_STORAGE_SIZE"), "disk cap of a user's container's writable layer, e.g. 10G, where the storage driver supports it (NUDGE_TENANT_STORAGE_SIZE)")
	deniedLabels := fs.String("tenant-denied-labels", envOr(getenv, "NUDGE_TENANT_DENIED_LABELS", DefaultDeniedLabels), "comma-separated label prefixes users may not set on their containers, or none (NUDGE_TENANT_DENIED_LABELS)")
	backupInterval, err := time.ParseDuration(envOr(getenv, "NUDGE_BACKUP_INTERVAL", "0"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_BACKUP_INTERVAL %q: want a duration such as 24h", getenv("NUDGE_BACKUP_INTERVAL"))
	}
	fs.DurationVar(&c.Backup.Interval, "backup-interval", backupInterval, "how often to save a backup to <data>/backups, 0 for never (NUDGE_BACKUP_INTERVAL)")
	backupKeep, err := strconv.Atoi(envOr(getenv, "NUDGE_BACKUP_KEEP", "7"))
	if err != nil {
		return Server{}, fmt.Errorf("NUDGE_BACKUP_KEEP %q: want a number", getenv("NUDGE_BACKUP_KEEP"))
	}
	fs.IntVar(&c.Backup.Keep, "backup-keep", backupKeep, "how many scheduled backups to keep (NUDGE_BACKUP_KEEP)")
	fs.StringVar(&c.Backup.PassphraseFile, "backup-passphrase-file", getenv("NUDGE_BACKUP_PASSPHRASE_FILE"), "file with the passphrase scheduled backups are encrypted with (NUDGE_BACKUP_PASSPHRASE_FILE)")
	level := fs.String("log-level", envOr(getenv, "NUDGE_LOG_LEVEL", "info"), "debug, info, warn or error (NUDGE_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return Server{}, err
	}
	c.NoPasswordLogin = !passwordLogin
	c.OIDC.ClientSecret = getenv("NUDGE_OIDC_CLIENT_SECRET")
	if fs.NArg() > 0 {
		return Server{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := c.LogLevel.UnmarshalText([]byte(*level)); err != nil {
		return Server{}, fmt.Errorf("log level %q: %w", *level, err)
	}
	c.AdminPassword = getenv("NUDGE_ADMIN_PASSWORD")
	if c.Addr == "" {
		return Server{}, errors.New("listen address must not be empty")
	}
	if c.DockerHost == "" {
		return Server{}, errors.New("docker host must not be empty")
	}
	if c.DataDir == "" {
		return Server{}, errors.New("data directory must not be empty")
	}
	if c.StacksDir == "" {
		c.StacksDir = filepath.Join(c.DataDir, "stacks")
	}
	if !filepath.IsAbs(c.StacksDir) {
		return Server{}, errors.New("stacks directory must be an absolute path")
	}
	if c.Compose == "" {
		return Server{}, errors.New("compose binary must not be empty")
	}
	if c.AuditRetentionDays < 1 {
		return Server{}, errors.New("audit retention must be at least 1 day")
	}
	if c.UpdateCheckInterval != 0 && c.UpdateCheckInterval < 5*time.Minute {
		return Server{}, errors.New("update check interval must be 0 (off) or at least 5m")
	}
	if c.UsageInterval != 0 && c.UsageInterval < time.Minute {
		return Server{}, errors.New("usage interval must be 0 (off) or at least 1m")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return Server{}, errors.New("set both -tls-cert and -tls-key (NUDGE_TLS_CERT, NUDGE_TLS_KEY) or neither")
	}
	if c.PublicURL != "" {
		u, err := parsePublicURL(c.PublicURL)
		if err != nil {
			return Server{}, err
		}
		c.PublicURL = u
	}
	if c.TrustedProxies, err = parseProxies(*proxyList); err != nil {
		return Server{}, err
	}
	if err := c.checkOIDC(); err != nil {
		return Server{}, err
	}
	if c.UserRegistries, err = parseRegistries(*userRegistries); err != nil {
		return Server{}, err
	}
	if c.Tenant.StorageSize, err = parseSize(*storageSize); err != nil {
		return Server{}, fmt.Errorf("tenant storage size %w", err)
	}
	if c.Tenant.DeniedLabels, err = parseLabelPrefixes(*deniedLabels); err != nil {
		return Server{}, err
	}
	if err := c.Tenant.check(); err != nil {
		return Server{}, err
	}
	if err := c.Backup.check(); err != nil {
		return Server{}, err
	}
	return c, nil
}

func (b Backup) check() error {
	if b.Interval == 0 {
		return nil
	}
	if b.Interval < 5*time.Minute {
		return errors.New("backup interval must be 0 (off) or at least 5m")
	}
	if b.Keep < 1 {
		return errors.New("backup keep must be at least 1")
	}
	if b.PassphraseFile == "" {
		return errors.New("scheduled backups need -backup-passphrase-file (NUDGE_BACKUP_PASSPHRASE_FILE)")
	}
	return nil
}

// Restore is `nudge restore`'s configuration.
type Restore struct {
	// File is the backup to restore.
	File      string
	DataDir   string
	StacksDir string
	// PassphraseFile holds the backup's passphrase.
	PassphraseFile string
	// Force moves existing data aside instead of refusing.
	Force bool
}

// LoadRestore parses `nudge restore`'s args like LoadServer; the data and stacks
// directories take the server's flags and variables.
func LoadRestore(args []string, getenv func(string) string, out io.Writer) (Restore, error) {
	var c Restore
	fs := flag.NewFlagSet("nudge restore", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&c.File, "file", "", "the backup file to restore (required)")
	fs.StringVar(&c.DataDir, "data", envOr(getenv, "NUDGE_DATA", "/data"), "data directory (NUDGE_DATA)")
	fs.StringVar(&c.StacksDir, "stacks-dir", getenv("NUDGE_STACKS_DIR"), "compose stacks directory (NUDGE_STACKS_DIR; default <data>/stacks)")
	fs.StringVar(&c.PassphraseFile, "passphrase-file", getenv("NUDGE_BACKUP_PASSPHRASE_FILE"), "file with the backup's passphrase (NUDGE_BACKUP_PASSPHRASE_FILE; required)")
	fs.BoolVar(&c.Force, "force", false, "move existing data aside to <data>/pre-restore-<time> instead of refusing")
	if err := fs.Parse(args); err != nil {
		return Restore{}, err
	}
	if fs.NArg() > 0 {
		return Restore{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if c.File == "" {
		return Restore{}, errors.New("-file is required")
	}
	if c.PassphraseFile == "" {
		return Restore{}, errors.New("-passphrase-file (NUDGE_BACKUP_PASSPHRASE_FILE) is required")
	}
	if c.DataDir == "" {
		return Restore{}, errors.New("data directory must not be empty")
	}
	if c.StacksDir == "" {
		c.StacksDir = filepath.Join(c.DataDir, "stacks")
	}
	if !filepath.IsAbs(c.StacksDir) {
		return Restore{}, errors.New("stacks directory must be an absolute path")
	}
	return c, nil
}

// parseLabelPrefixes reads -tenant-denied-labels: comma-separated label prefixes, in
// lower case as labels are matched without case, or none for an empty list.
func parseLabelPrefixes(s string) ([]string, error) {
	if strings.TrimSpace(s) == "none" {
		return []string{}, nil
	}
	var out []string
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" || strings.ContainsFunc(entry, func(r rune) bool { return unicode.IsSpace(r) || r == '=' }) {
			return nil, fmt.Errorf("tenant denied labels %q: want comma-separated label prefixes such as traefik.,caddy, or none", s)
		}
		if !slices.Contains(out, entry) {
			out = append(out, entry)
		}
	}
	return out, nil
}

// check refuses a cgroup parent or runtime the daemon would misread.
func (t Tenant) check() error {
	if p := t.CgroupParent; p != "" {
		rest := strings.ReplaceAll(p, "{id}", "")
		if !cgroupParent.MatchString(p) || rest == p || strings.ContainsAny(rest, "{}") || strings.Contains(p, "..") {
			return fmt.Errorf("tenant cgroup parent %q: want a slice name or cgroup path with {id}, e.g. nudge-u{id}.slice", p)
		}
		if strings.HasSuffix(p, ".slice") && strings.Contains(p, "/") {
			return fmt.Errorf("tenant cgroup parent %q: a systemd slice is a name, not a path", p)
		}
	}
	if t.Runtime != "" && !runtimeName.MatchString(t.Runtime) {
		return fmt.Errorf("tenant runtime %q: want a runtime name such as runsc", t.Runtime)
	}
	return nil
}

// minStorageSize is the smallest disk cap: below it most images can't even start.
const minStorageSize = 64 << 20

// parseSize parses a size such as 10G or 512MiB (binary units, any case) into bytes;
// empty is 0 (no cap).
func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	m := sizePattern.FindStringSubmatch(strings.ToUpper(s))
	if m == nil {
		return 0, fmt.Errorf("%q: want a size such as 10G or 512M", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q: too large", s)
	}
	shift := map[string]uint{"": 0, "K": 10, "M": 20, "G": 30, "T": 40}[m[2]]
	if n > (1<<62)>>shift {
		return 0, fmt.Errorf("%q: too large", s)
	}
	n <<= shift
	if n < minStorageSize {
		return 0, fmt.Errorf("%q: at least 64M", s)
	}
	return n, nil
}

// checkOIDC refuses a half-set SSO configuration. Without SSO the OIDC settings are
// cleared, so their defaults don't suggest it is on.
func (c *Server) checkOIDC() error {
	o := &c.OIDC
	if o.Issuer == "" && o.ClientID == "" && o.ClientSecret == "" {
		if c.NoPasswordLogin {
			return errors.New("password login can only be turned off with single sign-on (NUDGE_OIDC_ISSUER)")
		}
		*o = OIDC{}
		return nil
	}
	if o.Issuer == "" || o.ClientID == "" || o.ClientSecret == "" {
		return errors.New("single sign-on needs the issuer, client ID and client secret (NUDGE_OIDC_ISSUER, NUDGE_OIDC_CLIENT_ID, NUDGE_OIDC_CLIENT_SECRET)")
	}
	u, err := url.Parse(o.Issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("OIDC issuer %q: want an https:// URL", o.Issuer)
	}
	if !strings.HasPrefix(c.PublicURL, "https://") {
		return errors.New("single sign-on needs an https:// public URL (NUDGE_PUBLIC_URL) for its redirect URI")
	}
	if o.Name == "" || o.AdminRole == "" {
		return errors.New("OIDC name and admin role must not be empty")
	}
	if o.UserRole == o.AdminRole {
		return errors.New("OIDC user role must differ from the admin role")
	}
	if o.SessionTTL < 5*time.Minute {
		return errors.New("OIDC session TTL must be at least 5m")
	}
	return nil
}

// parseProxies parses a comma-separated list of CIDRs and single addresses (as /32 or
// /128). IPv4-mapped addresses become IPv4, as the peers they are compared with do.
func parseProxies(s string) ([]netip.Prefix, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		bad := fmt.Errorf("trusted proxy %q: want a CIDR such as 172.16.0.0/12 or an IP address", entry)
		var p netip.Prefix
		if strings.Contains(entry, "/") {
			var err error
			if p, err = netip.ParsePrefix(entry); err != nil {
				return nil, bad
			}
			if a := p.Addr(); a.Is4In6() {
				// Shorter than /96 would reach past the mapped range into plain IPv6.
				if p.Bits() < 96 {
					return nil, fmt.Errorf("trusted proxy %q: an IPv4-mapped prefix needs at least /96", entry)
				}
				p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
			}
		} else {
			a, err := netip.ParseAddr(entry)
			if err != nil || a.Zone() != "" {
				return nil, bad
			}
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// registryHost is a registry host name or IPv4 address with an optional port, as the
// first component of an image reference names it.
var registryHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?(:[0-9]{1,5})?$`)

// parseRegistries parses a comma-separated list of registry hosts (or "*"); empty is
// nil (the default).
func parseRegistries(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []string
	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry != "*" && !registryHost.MatchString(entry) {
			return nil, fmt.Errorf("user registry %q: want a registry host such as ghcr.io or registry.example.com:5000, or *", entry)
		}
		out = append(out, entry)
	}
	return out, nil
}

// Agent is the configuration of `nudge agent`.
type Agent struct {
	// ServerURL is the server's public URL, https:// (http:// only with Insecure).
	ServerURL *url.URL
	// JoinToken enrolls the agent on its first connect; afterwards agent.json is used.
	JoinToken string
	// DataDir holds agent.json (agent ID and secret).
	DataDir string
	// Insecure allows an http:// server URL.
	Insecure bool
	// LogLevel is debug, info, warn or error.
	LogLevel slog.Level
	// DockerHost is the Docker API streams are piped to, unix:// or tcp://.
	DockerHost string
	// StacksDir holds the stacks the server deploys here; empty flag means <DataDir>/stacks.
	// Absolute, mounted at the same path on the host.
	StacksDir string
	// Compose is the docker-compose binary that deploys them.
	Compose string
}

// LoadAgent parses the agent's args like LoadServer.
func LoadAgent(args []string, getenv func(string) string, out io.Writer) (Agent, error) {
	var c Agent
	fs := flag.NewFlagSet("nudge agent", flag.ContinueOnError)
	fs.SetOutput(out)
	server := fs.String("server", getenv("NUDGE_SERVER_URL"), "the server's public URL (NUDGE_SERVER_URL)")
	// No env default here: -h prints defaults, and the token is a secret.
	fs.StringVar(&c.JoinToken, "join-token", "", "one-time token from \"Add environment\", needed only on the first connect (NUDGE_JOIN_TOKEN)")
	fs.StringVar(&c.DataDir, "data", envOr(getenv, "NUDGE_DATA", "/data"), "data directory for agent.json (NUDGE_DATA)")
	insecure, err := strconv.ParseBool(envOr(getenv, "NUDGE_INSECURE", "false"))
	if err != nil {
		return Agent{}, fmt.Errorf("NUDGE_INSECURE %q: want true or false", getenv("NUDGE_INSECURE"))
	}
	fs.BoolVar(&c.Insecure, "insecure", insecure, "allow an http:// server URL, for testing only (NUDGE_INSECURE)")
	fs.StringVar(&c.DockerHost, "docker-host", envOr(getenv, "DOCKER_HOST", "unix:///var/run/docker.sock"), "Docker API to serve, unix:// or tcp:// (DOCKER_HOST)")
	fs.StringVar(&c.StacksDir, "stacks-dir", getenv("NUDGE_STACKS_DIR"), "directory of the stacks the server deploys here, mounted at the same path on the host (NUDGE_STACKS_DIR; default <data>/stacks)")
	fs.StringVar(&c.Compose, "compose", envOr(getenv, "NUDGE_COMPOSE", "/usr/local/bin/docker-compose"), "docker-compose binary that deploys stacks (NUDGE_COMPOSE)")
	level := fs.String("log-level", envOr(getenv, "NUDGE_LOG_LEVEL", "info"), "debug, info, warn or error (NUDGE_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return Agent{}, err
	}
	if fs.NArg() > 0 {
		return Agent{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if c.JoinToken == "" {
		c.JoinToken = getenv("NUDGE_JOIN_TOKEN")
	}
	if err := c.LogLevel.UnmarshalText([]byte(*level)); err != nil {
		return Agent{}, fmt.Errorf("log level %q: %w", *level, err)
	}
	if c.ServerURL, err = parseServerURL(*server, c.Insecure); err != nil {
		return Agent{}, err
	}
	if c.DockerHost == "" {
		return Agent{}, errors.New("docker host must not be empty")
	}
	if c.DataDir == "" {
		return Agent{}, errors.New("data directory must not be empty")
	}
	if c.StacksDir == "" {
		c.StacksDir = filepath.Join(c.DataDir, "stacks")
	}
	if !filepath.IsAbs(c.StacksDir) {
		return Agent{}, errors.New("stacks directory must be an absolute path")
	}
	if c.Compose == "" {
		return Agent{}, errors.New("compose binary must not be empty")
	}
	return c, nil
}

func parseServerURL(s string, insecure bool) (*url.URL, error) {
	if s == "" {
		return nil, errors.New("server URL is required (-server or NUDGE_SERVER_URL)")
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("server URL %q: %w", s, err)
	}
	switch {
	case u.Scheme == "http" && !insecure:
		return nil, fmt.Errorf("server URL %q: http:// sends the agent secret in clear text; use https:// or set -insecure for testing", s)
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("server URL %q: want https://", s)
	case u.Host == "":
		return nil, fmt.Errorf("server URL %q: missing host", s)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("server URL %q: want only scheme, host and an optional path", s)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawPath = ""
	return u, nil
}

// parsePublicURL accepts http(s)://host[:port] with an optional trailing slash and
// returns it without one. nudge serves from the root, so a path is refused.
func parsePublicURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("public URL %q: %w", s, err)
	}
	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return "", fmt.Errorf("public URL %q: want http:// or https://", s)
	case u.Host == "":
		return "", fmt.Errorf("public URL %q: missing host", s)
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return "", fmt.Errorf("public URL %q: want only scheme, host and port", s)
	case u.Path != "" && u.Path != "/":
		return "", fmt.Errorf("public URL %q: nudge serves from the root; a path is not supported", s)
	case !hostPort.MatchString(u.Host):
		return "", fmt.Errorf("public URL %q: invalid host", s)
	}
	return u.Scheme + "://" + u.Host, nil
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}
