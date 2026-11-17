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

// EnvValues lists the values of the variables in env, the text of a stack's .env, in
// order, in every form compose may print them (see envValueForms): as written and as
// compose reads them. Empty forms are left out, and a line's forms are distinct.
func EnvValues(env string) []string {
	var out []string
	for line := range strings.SplitSeq(env, "\n") {
		value, ok := rawEnvValue(line)
		if !ok {
			continue
		}
		var forms []string
		for _, v := range envValueForms(value) {
			if v != "" && !slices.Contains(forms, v) {
				forms = append(forms, v)
			}
		}
		out = append(out, forms...)
	}
	return out
}

// rawEnvValue returns the value of a variable line of a .env file as written (the text
// after "=", trimmed, quotes and comments kept); comments and blank lines have none.
func rawEnvValue(line string) (string, bool) {
	s := strings.TrimSpace(line)
	if s == "" || strings.HasPrefix(s, "#") {
		return "", false
	}
	if rest, found := strings.CutPrefix(s, "export "); found {
		s = strings.TrimSpace(rest)
	}
	name, value, ok := strings.Cut(s, "=")
	if !ok || strings.TrimSpace(name) == "" {
		return "", false
	}
	return strings.TrimSpace(value), true
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

// MergeEnv fills env, the text of a stack's .env, from repo, the text of an env file
// in its repository: env wins. A variable env sets keeps its line, whatever repo says.
// One env lacks, or has with an empty value, gets repo's line as written (keeping its
// quoting), but only if repo's value isn't empty: an example file's "NAME=" never adds
// or blanks a variable. Filled variables stay in place; added ones go at the end in
// repo's order. env's other lines (comments, blank lines) stay; repo's are dropped.
func MergeEnv(env, repo string) string {
	theirs := map[string]string{}
	var order []string
	for line := range strings.SplitSeq(repo, "\n") {
		name, value, ok := envLine(line)
		if !ok || value == "" {
			continue
		}
		if _, dup := theirs[name]; !dup {
			order = append(order, name)
		}
		theirs[name] = strings.TrimSpace(line)
	}
	seen := map[string]bool{}
	lines := strings.Split(env, "\n")
	for i, line := range lines {
		name, value, ok := envLine(line)
		if !ok {
			continue
		}
		seen[name] = true
		if t, set := theirs[name]; set && value == "" {
			lines[i] = t
		}
	}
	out := strings.Join(lines, "\n")
	for _, name := range order {
		if seen[name] {
			continue
		}
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += theirs[name] + "\n"
	}
	return out
}
