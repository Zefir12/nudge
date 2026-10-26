package deployments

import (
	"maps"
	"slices"
	"strings"
)

// SetVariables sets vars in env, the text of a stack's .env, and returns the new text
// with the values they replaced ("" for a variable env didn't have). A variable's line
// ("NAME=…" or "export NAME=…") is replaced in place, so comments and the other lines
// stay; variables env lacks are added at the end, by name.
func SetVariables(env string, vars map[string]string) (string, map[string]string) {
	previous := map[string]string{}
	seen := map[string]bool{}
	lines := strings.Split(env, "\n")
	for i, line := range lines {
		name, value, ok := envLine(line)
		if !ok {
			continue
		}
		v, set := vars[name]
		if !set {
			continue
		}
		if !seen[name] {
			previous[name] = value
		}
		seen[name] = true
		lines[i] = name + "=" + v
	}
	out := strings.Join(lines, "\n")
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if seen[name] {
			continue
		}
		previous[name] = ""
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += name + "=" + vars[name] + "\n"
	}
	return out, previous
}

// envLine parses a variable line of a .env file; comments and blank lines aren't.
func envLine(line string) (name, value string, ok bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", "", false
	}
	if rest, found := strings.CutPrefix(s, "export "); found {
		s = strings.TrimSpace(rest)
	}
	name, value, ok = strings.Cut(s, "=")
	if !ok {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	return name, value, name != ""
}
