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
	"strconv"
	"strings"
	"time"
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
}

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
	return c, nil
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
