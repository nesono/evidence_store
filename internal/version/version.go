// What build of the server is running.
//
// The answer is a timestamp rather than a semantic version because nothing here
// is released: the store is deployed from a branch, and the useful question is
// not "which release is this" but "is this the build I deployed on Tuesday".
// A minute is fine enough to answer that and coarse enough to be readable —
// issue #111 asks for exactly this shape.
package version

import (
	"runtime/debug"
	"strconv"
	"time"
)

// Layout is the format the version is written in: the minute the build was
// made, in UTC. 2026.08.25.21.50.
const Layout = "2006.01.02.15.04"

// Unknown is what a build says when it cannot honestly claim a time.
const Unknown = "dev"

// stamp is set at link time by the Dockerfile:
//
//	-ldflags "-X github.com/nesono/evidence-store/internal/version.stamp=$(date -u +%Y.%m.%d.%H.%M)"
//
// It is empty in a `go run` or `bazel run` build, which is the case Resolve
// falls back for.
var stamp string

// Where a version came from. Worth reporting alongside the version itself,
// because "dev" is otherwise indistinguishable from a deployment somebody
// forgot to stamp, and the two need different things done about them.
const (
	// SourceBuild: stamped when the binary was linked. What a deployment runs.
	SourceBuild = "build"
	// SourceCommit: taken from the commit the binary was built from, for a
	// build that was not stamped but came from a clean checkout.
	SourceCommit = "commit"
	// SourceUnknown: neither. A working copy, or a build with nothing to go on.
	SourceUnknown = "unknown"
)

// Version is what the server calls itself.
type Version struct {
	Version string `json:"version"`
	Source  string `json:"source"`
}

// Current reports the running build.
func Current() Version {
	info, ok := debug.ReadBuildInfo()
	return Resolve(stamp, info, ok)
}

// Resolve works out the version from what a build left behind. Separated from
// Current so the decisions can be tested without linking a binary for each one.
//
// The order is by how much each source actually knows. A link-time stamp is the
// build, exactly. A commit time is when the code was written, which is close
// enough to be useful and honest enough to label differently. A working copy
// with uncommitted changes matches no commit at all, so it claims neither.
func Resolve(stamp string, info *debug.BuildInfo, ok bool) Version {
	if stamp != "" {
		return Version{Version: stamp, Source: SourceBuild}
	}
	if !ok || info == nil {
		return Version{Version: Unknown, Source: SourceUnknown}
	}

	var revisionTime string
	var modified bool
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.time":
			revisionTime = setting.Value
		case "vcs.modified":
			modified, _ = strconv.ParseBool(setting.Value)
		}
	}

	// A dirty tree is not the commit it sits on, and dating the running code by
	// a commit it does not match would be a plausible-looking untruth — the
	// kind that costs an hour when somebody is working out which build has the
	// bug.
	if revisionTime == "" || modified {
		return Version{Version: Unknown, Source: SourceUnknown}
	}

	at, err := time.Parse(time.RFC3339, revisionTime)
	if err != nil {
		return Version{Version: Unknown, Source: SourceUnknown}
	}
	return Version{Version: Format(at), Source: SourceCommit}
}

// Format writes a moment the way a version is spelled.
func Format(t time.Time) string {
	return t.UTC().Format(Layout)
}
