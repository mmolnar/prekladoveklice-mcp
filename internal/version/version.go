// Package version carries build information. Release builds set Version and
// Commit via -ldflags; `go install ...@vX.Y.Z` builds fall back to the
// module version recorded in the binary.
package version

import (
	"runtime"
	"runtime/debug"
)

var (
	Version = "dev"
	Commit  = ""
	Repo    = "https://github.com/mmolnar/prekladoveklice-mcp"
)

func init() {
	if Version != "dev" {
		return
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		Version = v
	}
	if Commit == "" {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				Commit = s.Value[:7]
			}
		}
	}
}

// UserAgent identifies pk to the source website.
func UserAgent() string {
	return "pk/" + Version + " (+" + Repo + "; " + runtime.GOOS + ")"
}
