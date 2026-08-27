package version

import (
	"runtime/debug"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func buildInfo(settings map[string]string) *debug.BuildInfo {
	info := &debug.BuildInfo{}
	for key, value := range settings {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: key, Value: value})
	}
	return info
}

func TestStampWins(t *testing.T) {
	// A link-time stamp is the build, exactly, and nothing else knows better.
	got := Resolve("2026.08.25.21.50", buildInfo(map[string]string{"vcs.time": "2020-01-01T00:00:00Z"}), true)

	assert.Equal(t, "2026.08.25.21.50", got.Version)
	assert.Equal(t, SourceBuild, got.Source)
}

func TestFallsBackToTheCommitItWasBuiltFrom(t *testing.T) {
	got := Resolve("", buildInfo(map[string]string{
		"vcs.time":     "2026-08-25T21:50:31Z",
		"vcs.modified": "false",
	}), true)

	assert.Equal(t, "2026.08.25.21.50", got.Version)
	// Labelled differently from a stamp, because it is a different claim: when
	// the code was written, not when this binary was made.
	assert.Equal(t, SourceCommit, got.Source)
}

func TestCommitTimeIsNormalisedToUTC(t *testing.T) {
	got := Resolve("", buildInfo(map[string]string{
		"vcs.time":     "2026-08-25T23:50:00+02:00",
		"vcs.modified": "false",
	}), true)

	assert.Equal(t, "2026.08.25.21.50", got.Version,
		"two people in different time zones must read the same version off the same build")
}

func TestUncommittedChangesClaimNothing(t *testing.T) {
	// A working copy is not the commit it sits on. Dating the running code by a
	// commit it does not match is the kind of plausible-looking untruth that
	// costs an hour when somebody is working out which build has the bug.
	got := Resolve("", buildInfo(map[string]string{
		"vcs.time":     "2026-08-25T21:50:00Z",
		"vcs.modified": "true",
	}), true)

	assert.Equal(t, Unknown, got.Version)
	assert.Equal(t, SourceUnknown, got.Source)
}

func TestNothingToGoOn(t *testing.T) {
	for _, tt := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
	}{
		{"no build info at all", nil, false},
		{"build info without VCS settings", buildInfo(nil), true},
		{"an unparseable commit time", buildInfo(map[string]string{"vcs.time": "last Tuesday"}), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve("", tt.info, tt.ok)
			assert.Equal(t, Unknown, got.Version)
			assert.Equal(t, SourceUnknown, got.Source)
		})
	}
}

func TestFormatIsTheShapeTheIssueAsksFor(t *testing.T) {
	at := time.Date(2026, 8, 25, 21, 50, 59, 0, time.UTC)
	assert.Equal(t, "2026.08.25.21.50", Format(at))

	// Zero-padded throughout, so versions sort lexically in the order they were
	// built — which is how anybody reading a list of them will expect it.
	assert.Equal(t, "2026.01.02.03.04", Format(time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)))
}

func TestCurrentAlwaysAnswers(t *testing.T) {
	// Whatever this test binary was built from, the server must have something
	// to report rather than an empty string in the page footer.
	assert.NotEmpty(t, Current().Version)
	assert.NotEmpty(t, Current().Source)
}
