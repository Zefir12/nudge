// Package version holds the build's version, set at link time:
// go build -ldflags "-X git.zefirlabs.net/zefir/nudge/backend/internal/version.Version=v1.2.3"
package version

// Version is the release tag or commit the binary was built from.
var Version = "dev"
