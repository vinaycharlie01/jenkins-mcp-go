// Package version holds build metadata injected via -ldflags.
package version

var (
	// Version is the release version.
	Version = "dev"
	// GitCommit is the short commit hash.
	GitCommit = "unknown"
)
