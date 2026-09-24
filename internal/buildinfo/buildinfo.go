// Package buildinfo holds version information injected at build time via ldflags.
package buildinfo

import "strings"

// Set via -ldflags at build time:
//
//	go build -ldflags "-X prism/internal/buildinfo.Version=1.0.0 ..."
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildTime = "unknown"

	// Tags is the comma-separated build tag set the binary was compiled with.
	Tags = ""
)

// TagList returns the build tags as a slice. An empty Tags value yields nil.
func TagList() []string {
	if Tags == "" {
		return nil
	}
	parts := make([]string, 0, 8)
	for _, part := range strings.Split(Tags, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
}
