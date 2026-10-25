package registryclient

import (
	"regexp"
	"strings"
)

// The distribution reference grammar (github.com/distribution/reference), anchored. A
// name that matches has no "..", no "%" and no empty path component, so it can go into
// a URL path as it is.
var (
	pathComponent = `[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*`
	repository    = regexp.MustCompile(`^` + pathComponent + `(?:/` + pathComponent + `)*$`)
	tag           = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digest        = regexp.MustCompile(`^(?:sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`)
)

// maxRepository is the longest repository name distribution accepts.
const maxRepository = 255

// ValidRepository reports whether name is a repository name such as zefir/nudge.
func ValidRepository(name string) bool {
	return len(name) <= maxRepository && repository.MatchString(name)
}

// ValidTag reports whether name is a tag such as latest or v1.2.3. Forgejo's platform
// manifests (versions named sha256:…) are not tags.
func ValidTag(name string) bool { return tag.MatchString(name) }

// ValidDigest reports whether d is a content digest such as sha256:… (lowercase hex).
func ValidDigest(d string) bool { return digest.MatchString(d) }

// splitOwner splits a Forgejo repository owner/name into its owner and package name.
func splitOwner(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(repo, "/")
	return owner, name, ok && owner != "" && name != ""
}
