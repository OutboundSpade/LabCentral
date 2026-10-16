package cli

import (
	"runtime/debug"
	"strings"
)

// Version is set by release builds with -ldflags "-X .../internal/cli.Version=v2.x.y".
var Version string

// version reports the release version, or the module version or commit that Go
// recorded for other builds.
func version() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	revision, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if revision == "" {
		return "development build"
	}
	revision = revision[:min(len(revision), 12)]
	if dirty {
		revision += "-dirty"
	}
	return strings.Join([]string{"development build", revision}, " ")
}
