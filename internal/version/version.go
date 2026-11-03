// Package version carries build information, set via -ldflags at release time.
package version

import "runtime"

var (
	Version = "dev"
	Commit  = ""
	Repo    = "https://github.com/mmolnar/prekladoveklice-mcp"
)

// UserAgent identifies pk to the source website.
func UserAgent() string {
	return "pk/" + Version + " (+" + Repo + "; " + runtime.GOOS + ")"
}
