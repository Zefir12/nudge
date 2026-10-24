package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// credentialsFile is the name of the credentials file in the data directory.
const credentialsFile = "agent.json"

// Credentials are what enrolling returns and every connect presents.
type Credentials struct {
	EnvironmentID int64  `json:"environment_id"`
	AgentID       string `json:"agent_id"`
	Secret        string `json:"agent_secret"`
}

var errNoCredentials = errors.New("no credentials")

func (a *Agent) credsPath() string { return filepath.Join(a.opts.DataDir, credentialsFile) }

// loadCredentials reads path; errNoCredentials if it doesn't exist.
func loadCredentials(path string) (*Credentials, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the path is the configured data directory
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("read agent credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("agent credentials %s: %w", path, err)
	}
	if c.AgentID == "" || c.Secret == "" {
		return nil, fmt.Errorf("agent credentials %s: missing agent_id or agent_secret", path)
	}
	return &c, nil
}

// saveCredentials writes c to path (0600) through a temporary file and a rename, so a
// crash never leaves a truncated file behind.
func saveCredentials(path string, c *Credentials) (err error) {
	b, err := json.MarshalIndent(c, "", "  ") // #nosec G117 -- agent.json is where the secret belongs (0600)
	if err != nil {
		return fmt.Errorf("encode agent credentials: %w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".agent-*.json") // CreateTemp makes it 0600
	if err != nil {
		return fmt.Errorf("save agent credentials: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if _, err = f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("save agent credentials: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("save agent credentials: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("save agent credentials: %w", err)
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return fmt.Errorf("save agent credentials: %w", err)
	}
	return nil
}

// postEnroll exchanges the join token for credentials.
func (a *Agent) postEnroll(ctx context.Context) (*Credentials, error) {
	body, err := json.Marshal(map[string]string{"join_token": a.opts.JoinToken})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint("/api/v1/agents/enroll").String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.opts.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
	case http.StatusUnauthorized:
		return nil, errUnauthorized
	default:
		return nil, fmt.Errorf("server answered %s", resp.Status)
	}
	var c Credentials
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&c); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if c.AgentID == "" || c.Secret == "" {
		return nil, errors.New("response without agent_id or agent_secret")
	}
	return &c, nil
}
