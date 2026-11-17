package stacks

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// maxConfigOutput bounds what `compose config` may print for the bind check; a bigger
// project is deployed unchecked.
const maxConfigOutput = 8 << 20

// configArgs is the bind check's compose subcommand. --no-env-resolution leaves the
// services' env_file entries unread: bind sources don't depend on them, and the output
// then holds no values of the stack's .env. Interpolation still reads .env, as up does.
var configArgs = []string{"config", "--format", "json", "--no-env-resolution"}

// Bounds on the stderr of a failed `compose config` that is logged: maxConfigStderr is
// read, its first maxLoggedStderr bytes logged once redacted. Reading more than is logged
// keeps a secret cut at the read limit out of the log.
const (
	maxConfigStderr = 4 << 10
	maxLoggedStderr = 1 << 10
)

// minRedacted is the shortest .env value redacted from logged compose errors; shorter
// ones ("1", "yes") are no secrets and masking them would garble the message.
const minRedacted = 4

// Mount is one mount of the container nudge runs in.
type Mount struct {
	// Type is "bind", "volume", "tmpfs", ...
	Type string
	// Name is a volume's name.
	Name string
	// Source is the host path of a bind (of a volume, its directory on the host).
	Source      string
	Destination string
}

// MountsFunc returns the mounts of the container nudge runs in; in is false when nudge
// doesn't run in a container the daemon knows, and then every bind can be mounted.
type MountsFunc func(ctx context.Context) (mounts []Mount, in bool, err error)

// BindError means a stack binds a path in its directory that the daemon would take from
// a different place on the host: nudge has the stacks directory from a volume, from
// another host path, or from its own filesystem.
type BindError struct {
	// Source is the bind source as the stack names it, relative to its directory.
	Source string
	// Path is the absolute path in nudge's container, which the daemon reads on the host.
	Path string
	// Mount is the mount of nudge's container that holds Path, nil if none does.
	Mount *Mount
}

func (e *BindError) Error() string {
	var from string
	switch {
	case e.Mount == nil:
		from = "from its own filesystem, not from a mount"
	case e.Mount.Type == "volume":
		from = fmt.Sprintf("from the volume %s mounted at %s", e.Mount.Name, e.Mount.Destination)
	case e.Mount.Type == "bind":
		from = fmt.Sprintf("from the host's %s mounted at %s", e.Mount.Source, e.Mount.Destination)
	default:
		from = fmt.Sprintf("from a %s mount at %s", e.Mount.Type, e.Mount.Destination)
	}
	return fmt.Sprintf("the bind mount %s can't be deployed: the daemon takes %s from the host, but nudge has it %s; "+
		"mount the stacks directory at the same path on the host and set NUDGE_STACKS_DIR to it", e.Source, e.Path, from)
}

// checkBinds returns a *BindError if stack name binds a path in the stacks directory
// that the daemon would take from a different place on the host. Without l.mounts it
// passes.
//
// Policy: only a definite "can't be mounted" refuses the deploy. Anything that keeps the
// check from deciding (compose fails to run or exits non-zero, its output is too big or
// unreadable, nudge's mounts can't be looked up) is logged as a warning and the deploy
// goes on; up then reports its own errors.
func (l *Local) checkBinds(ctx context.Context, name string) *BindError {
	if l.mounts == nil {
		return nil
	}
	skip := func(reason string, args ...any) *BindError {
		l.log.Warn("stack bind check skipped: "+reason, append([]any{"stack", name}, args...)...)
		return nil
	}
	out, stderr := &cappedBuffer{max: maxConfigOutput}, &cappedBuffer{max: maxConfigStderr}
	code, err := l.exec(ctx, l.command(name, configArgs, out, stderr))
	switch {
	case err != nil:
		return skip("compose config did not run", "err", err, "stderr", l.redactedStderr(name, stderr))
	case code != 0:
		return skip("compose config failed", "exit_code", code, "stderr", l.redactedStderr(name, stderr))
	case out.over:
		return skip("compose config output too large", "limit", maxConfigOutput)
	}
	root := filepath.Clean(l.dir.root)
	sources, err := bindSources(out.Bytes(), root)
	if err != nil {
		return skip("compose config output unreadable", "err", err)
	}
	if len(sources) == 0 {
		return nil
	}
	mounts, in, err := l.mounts(ctx)
	if err != nil {
		return skip("mounts of nudge's container unknown", "err", err)
	}
	if !in {
		return nil
	}
	dir := l.dir.Path(name)
	for _, src := range sources {
		m := covering(mounts, src)
		if m != nil && m.Type == "bind" && filepath.Clean(m.Source) == filepath.Clean(m.Destination) {
			continue
		}
		return &BindError{Source: relSource(dir, src), Path: src, Mount: m}
	}
	return nil
}

// redactedStderr is what compose wrote to stderr, for a log: the values of stack name's
// .env masked and cut to maxLoggedStderr bytes.
func (l *Local) redactedStderr(name string, stderr *cappedBuffer) string {
	var secrets []string
	if files, err := l.dir.Read(name); err == nil {
		secrets = envValues(files.Env)
	}
	text := strings.TrimSpace(mask(stderr.String(), secrets))
	if len(text) > maxLoggedStderr {
		cut := maxLoggedStderr
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "..."
	}
	return text
}

// envValues returns the values in a .env file of at least minRedacted bytes, as written
// and as compose reads them (see envValueForms), longest first so a short value can't
// break up a longer one containing it.
func envValues(env string) []string {
	var values []string
	for line := range strings.Lines(env) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		for _, v := range envValueForms(strings.TrimSpace(value)) {
			if len(v) >= minRedacted {
				values = append(values, v)
			}
		}
	}
	slices.SortFunc(values, func(a, b string) int {
		if n := cmp.Compare(len(b), len(a)); n != 0 {
			return n
		}
		return strings.Compare(a, b)
	})
	return slices.Compact(values)
}

// envValueForms returns the forms of a .env value (the text after "=", trimmed) that
// compose's stderr may echo: the value as written, and the value compose reads. A
// quoted value is the text up to the matching closing quote (a double-quoted one also
// with its escapes decoded); an unquoted one ends before an inline " #" comment.
//
// Best effort: a quoted value that doesn't close on its line spans several lines; only
// its first line is returned, and mask doesn't match across lines anyway.
func envValueForms(value string) []string {
	forms := []string{value}
	if value == "" {
		return forms
	}
	switch q := value[0]; q {
	case '\'', '"':
		end := closingQuote(value, q)
		if end < 0 {
			return append(forms, value[1:])
		}
		inner := value[1:end]
		forms = append(forms, inner)
		if q == '"' {
			if decoded := unescapeDouble(inner); decoded != inner {
				forms = append(forms, decoded)
			}
		}
	default:
		if i := inlineComment(value); i >= 0 {
			forms = append(forms, strings.TrimSpace(value[:i]))
		}
	}
	return forms
}

// inlineComment returns the index of the "#" that starts an inline comment in an
// unquoted value (one after a space or tab), or -1.
func inlineComment(value string) int {
	for i := 1; i < len(value); i++ {
		if value[i] == '#' && (value[i-1] == ' ' || value[i-1] == '\t') {
			return i
		}
	}
	return -1
}

// closingQuote returns the index of the quote q that closes value (which starts with q),
// or -1. Inside double quotes a backslash escapes the next byte.
func closingQuote(value string, q byte) int {
	for i := 1; i < len(value); i++ {
		switch {
		case q == '"' && value[i] == '\\':
			i++
		case value[i] == q:
			return i
		}
	}
	return -1
}

// unescapeDouble decodes the backslash escapes of a double-quoted .env value.
func unescapeDouble(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// relSource names src as the stack in dir would: "./data", "../shared/data", ".".
func relSource(dir, src string) string {
	rel, err := filepath.Rel(dir, src)
	switch {
	case err != nil:
		return src
	case rel == "." || rel == ".." || strings.HasPrefix(rel, "../"):
		return filepath.ToSlash(rel)
	}
	return "./" + filepath.ToSlash(rel)
}

// bindSources returns the bind sources of the project compose config printed as JSON
// that are under root (the stacks directory), sorted: service volumes of type bind and
// the file secrets and configs services use (compose bind-mounts those too).
func bindSources(config []byte, root string) ([]string, error) {
	type ref struct {
		Source string `json:"source"`
	}
	type file struct {
		File string `json:"file"`
	}
	var p struct {
		Services map[string]struct {
			Volumes []struct {
				Type   string `json:"type"`
				Source string `json:"source"`
			} `json:"volumes"`
			Secrets []ref `json:"secrets"`
			Configs []ref `json:"configs"`
		} `json:"services"`
		Secrets map[string]file `json:"secrets"`
		Configs map[string]file `json:"configs"`
	}
	if err := json.Unmarshal(config, &p); err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var all []string
	for _, s := range p.Services {
		for _, v := range s.Volumes {
			if v.Type == "bind" {
				all = append(all, v.Source)
			}
		}
		for _, r := range s.Secrets {
			all = append(all, p.Secrets[r.Source].File)
		}
		for _, r := range s.Configs {
			all = append(all, p.Configs[r.Source].File)
		}
	}
	sources := []string{}
	for _, src := range all {
		if src != "" && filepath.IsAbs(src) && within(root, filepath.Clean(src)) {
			sources = append(sources, filepath.Clean(src))
		}
	}
	slices.Sort(sources)
	return slices.Compact(sources), nil
}

// covering returns the mount that holds path: the one with the longest destination
// path is in, nil if none.
func covering(mounts []Mount, path string) *Mount {
	var best *Mount
	for i := range mounts {
		m := &mounts[i]
		if m.Destination == "" || !within(filepath.Clean(m.Destination), path) {
			continue
		}
		if best == nil || len(filepath.Clean(m.Destination)) > len(filepath.Clean(best.Destination)) {
			best = m
		}
	}
	return best
}

// within reports whether path is dir or below it; both are clean and absolute.
func within(dir, path string) bool {
	if dir == "/" || path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+"/")
}

// cappedBuffer keeps the first max bytes written and notes whether more were.
type cappedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); len(p) > room {
		b.over = true
		_, _ = b.Buffer.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
