package deployments

import "strings"

// The parsing below is a copy of the one in stacks/binds.go (envValueForms and its
// helpers); the two should become one shared helper.

// envValueForms returns the forms of a .env value (the text after "=", trimmed) that
// compose's output may echo: the value as written, and the value compose reads. A
// quoted value is the text up to the matching closing quote (a double-quoted one also
// with its escapes decoded); an unquoted one ends before an inline " #" comment.
//
// Best effort: a quoted value that doesn't close on its line spans several lines; only
// its first line is returned, and output lines are matched one at a time anyway.
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
