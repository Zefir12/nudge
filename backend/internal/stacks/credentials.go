package stacks

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Registry credentials reach compose through a Docker config of its own: for each
// `compose up`, a config.json (0600) in a new private temporary directory (0700) named by
// DOCKER_CONFIG, removed when compose exits. The directories live in LocalOptions.ConfigDir
// (`<data>/compose-config`), which CleanConfigDir sweeps at start in case a killed
// process left one. Their passwords are masked in compose's output.

// dockerHub is the address nudge stores Docker Hub's credentials under, and
// dockerHubKey the one Docker configs use for it.
const (
	dockerHub    = "docker.io"
	dockerHubKey = "https://index.docker.io/v1/"
)

// masked replaces secrets in compose's output.
const masked = "********"

// Credential is a registry login compose may pull with.
type Credential struct {
	Address  string `json:"address"`
	Username string `json:"username"`
	Password string `json:"password"` // #nosec G117 -- sent to the agent's compose by design
}

// String names the credential without its password.
func (c Credential) String() string { return c.Username + "@" + c.Address }

// GoString is String, so %#v doesn't print the password either.
func (c Credential) GoString() string { return c.String() }

// LogValue logs the credential without its password.
func (c Credential) LogValue() slog.Value {
	return slog.GroupValue(slog.String("address", c.Address), slog.String("username", c.Username))
}

// CredentialFunc returns the credentials compose may need to pull the images of a stack
// with files f.
type CredentialFunc func(ctx context.Context, f Files) ([]Credential, error)

type credentialsKey struct{}

// withCredentials hands creds to the Local runner a control service calls with ctx.
func withCredentials(ctx context.Context, creds []Credential) context.Context {
	if len(creds) == 0 {
		return ctx
	}
	return context.WithValue(ctx, credentialsKey{}, creds)
}

func credentialsFrom(ctx context.Context) []Credential {
	creds, _ := ctx.Value(credentialsKey{}).([]Credential)
	return creds
}

// basicAuth is a Docker config's "auth": user:password in base64.
func basicAuth(c Credential) string {
	return base64.StdEncoding.EncodeToString([]byte(c.Username + ":" + c.Password))
}

// configPrefix starts the name of each temporary Docker config directory.
const configPrefix = "nudge-compose-"

// CleanConfigDir makes dir (0700) for the temporary Docker configs and removes the ones a
// killed run left there, returning how many. Call it at start, before any compose run.
func CleanConfigDir(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, fmt.Errorf("docker config directory: %w", err)
	}
	// MkdirAll leaves an existing directory's mode alone.
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- a directory: 0700 is owner-only
		return 0, fmt.Errorf("docker config directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("docker config directory: %w", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), configPrefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return n, fmt.Errorf("remove stale docker config: %w", err)
		}
		n++
	}
	return n, nil
}

// writeDockerConfig writes creds to config.json in a new private temporary directory in
// parent (empty: the system's) and returns it; the caller removes it.
func writeDockerConfig(parent string, creds []Credential) (string, error) {
	type auth struct {
		Auth string `json:"auth"`
	}
	auths := make(map[string]auth, len(creds))
	for _, c := range creds {
		key := c.Address
		if key == dockerHub {
			key = dockerHubKey
		}
		auths[key] = auth{Auth: basicAuth(c)}
	}
	// Marshalling a map of strings can't fail.
	b, _ := json.Marshal(map[string]any{"auths": auths})
	dir, err := os.MkdirTemp(parent, configPrefix)
	if err != nil {
		return "", fmt.Errorf("docker config: %w", err)
	}
	if err := writePrivate(filepath.Join(dir, "config.json"), b); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("docker config: %w", err)
	}
	return dir, nil
}

// writePrivate creates path with mode 0600 and writes b to it.
func writePrivate(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- a file in our own temporary directory
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// secretsOf returns what of creds must not show in compose's output, once each and
// longest first, so masking a short password can't break up an auth that contains it.
func secretsOf(creds []Credential) []string {
	var secrets []string
	for _, c := range creds {
		if c.Password != "" {
			secrets = append(secrets, c.Password, basicAuth(c))
		}
	}
	slices.SortFunc(secrets, func(a, b string) int {
		if n := cmp.Compare(len(b), len(a)); n != 0 {
			return n
		}
		return strings.Compare(a, b)
	})
	return slices.Compact(secrets)
}

// mask replaces each of secrets in s, in order. A secret is matched within one line, so
// a password with a newline in it isn't masked.
func mask(s string, secrets []string) string {
	for _, secret := range secrets {
		s = strings.ReplaceAll(s, secret, masked)
	}
	return s
}
