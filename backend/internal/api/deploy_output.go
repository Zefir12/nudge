package api

import (
	"fmt"
	"strings"

	"git.zefirlabs.net/zefir/nudge/backend/internal/deployments"
	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

// Bounds of the compose output a deployment run keeps to say why compose failed.
const (
	// composeTailLines is how many of compose's last lines a run keeps.
	composeTailLines = 20
	// composeTailBytes caps the kept lines together; the oldest go first.
	composeTailBytes = 4096
	// composeLineBytes caps one kept line; a longer one is cut.
	composeLineBytes = 512
	// minSecretLen is the shortest .env value whose lines are hidden. Shorter values
	// ("1", "yes") are no secrets, and would hide nearly every line.
	minSecretLen = 4
)

// hiddenComposeLine stands for a line of compose's output that held a .env value.
const hiddenComposeLine = "[a line was hidden: it contains a value from the stack's .env]"

// composeTail keeps the last lines of one compose run's output, both streams, so a
// failed run can show why compose failed. Not safe for concurrent use: compose's output
// comes through one callback.
type composeTail struct {
	lines []string
	size  int
}

// add keeps l, dropping the oldest lines over the bounds.
func (t *composeTail) add(l stacks.Line) {
	text := strings.TrimSpace(l.Text)
	if text == "" {
		return
	}
	if len(text) > composeLineBytes {
		text = strings.ToValidUTF8(text[:composeLineBytes], "") + "…"
	}
	t.lines = append(t.lines, text)
	t.size += len(text)
	for len(t.lines) > composeTailLines || t.size > composeTailBytes {
		t.size -= len(t.lines[0])
		t.lines = t.lines[1:]
	}
}

// redacted returns the kept lines with every line that contains one of secrets replaced
// by hiddenComposeLine.
func (t *composeTail) redacted(secrets []string) []string {
	out := make([]string, 0, len(t.lines))
	for _, l := range t.lines {
		if containsAny(l, secrets) {
			l = hiddenComposeLine
		}
		out = append(out, l)
	}
	return out
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// envSecrets are the values compose's output must not show: those of the given texts of
// a stack's .env (before and after a run), minSecretLen bytes or longer. The values of
// the run's own variables are left out, as the run shows them anyway (an image tag is
// often what compose's error is about), unless another variable holds the same value.
func envSecrets(run map[string]string, envs ...string) []string {
	public := map[string]int{}
	for _, v := range run {
		public[v]++
	}
	var out []string
	seen := map[string]bool{}
	for _, env := range envs {
		counts := map[string]int{}
		for _, v := range deployments.EnvValues(env) {
			counts[v]++
		}
		for v, n := range counts {
			if seen[v] || len(v) < minSecretLen || n <= public[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// withComposeLine adds compose's last line to msg, if there is one.
func withComposeLine(msg string, lines []string) string {
	if len(lines) == 0 {
		return msg
	}
	return msg + ": " + lines[len(lines)-1]
}

// composeExit is a compose up that exited with an error code.
type composeExit struct {
	code int
}

func (e *composeExit) Error() string { return fmt.Sprintf("compose exited with code %d", e.code) }
