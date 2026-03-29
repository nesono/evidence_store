package testlogs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathToTarget(t *testing.T) {
	tests := []struct {
		rel    string
		target string
	}{
		{"pkg/my_test/test.xml", "//pkg:my_test"},
		{"foo/bar/baz_test/test.xml", "//foo/bar:baz_test"},
		{"root_test/test.xml", "//:root_test"},
		{"pkg/my_test/shard_1_of_2/test.xml", "//pkg:my_test"},
		{"a/b/c/d_test/shard_3_of_4/test.xml", "//a/b/c:d_test"},
	}

	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			got, err := pathToTarget(tt.rel)
			require.NoError(t, err)
			assert.Equal(t, tt.target, got)
		})
	}
}

func TestScan(t *testing.T) {
	// Create a temporary testlogs directory structure.
	dir := t.TempDir()

	// //pkg:my_test (not cached)
	mkTestXML(t, dir, "pkg/my_test/test.xml")

	// //foo/bar:baz_test (cached)
	mkTestXML(t, dir, "foo/bar/baz_test/test.xml")
	os.WriteFile(filepath.Join(dir, "foo/bar/baz_test/test.cache_status"), []byte("REMOTE_CACHE_HIT"), 0644)

	// //pkg:sharded_test shard 1
	mkTestXML(t, dir, "pkg/sharded_test/shard_1_of_2/test.xml")
	// //pkg:sharded_test shard 2
	mkTestXML(t, dir, "pkg/sharded_test/shard_2_of_2/test.xml")

	entries, err := Scan(dir)
	require.NoError(t, err)
	require.Len(t, entries, 4)

	// Build a map for easier assertion.
	byPath := make(map[string]TestLogEntry)
	for _, e := range entries {
		byPath[e.XMLPath] = e
	}

	// Check targets.
	targets := make(map[string]bool)
	for _, e := range entries {
		targets[e.BazelTarget] = true
	}
	assert.True(t, targets["//pkg:my_test"])
	assert.True(t, targets["//foo/bar:baz_test"])
	assert.True(t, targets["//pkg:sharded_test"])

	// Check cache status.
	for _, e := range entries {
		if e.BazelTarget == "//foo/bar:baz_test" {
			assert.True(t, e.WasCached)
		}
	}
}

func TestResultFromLog(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected string
		ok       bool
	}{
		{"pass", "exec ${PAGER:-/usr/bin/less} \"$0\" || exit 1\nExecuting tests from //pkg:test\n-----------------------------------------------------------------------------\nPASS\n", "PASS", true},
		{"fail", "exec ${PAGER:-/usr/bin/less} \"$0\" || exit 1\nExecuting tests from //pkg:test\n-----------------------------------------------------------------------------\nFAIL\n", "FAIL", true},
		{"empty", "", "", false},
		{"unknown", "some random output\n", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "test.log")
			require.NoError(t, os.WriteFile(logPath, []byte(tt.content), 0644))

			result, ok := ResultFromLog(logPath)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestResultFromLogMissing(t *testing.T) {
	_, ok := ResultFromLog("/nonexistent/test.log")
	assert.False(t, ok)
}

func mkTestXML(t *testing.T, base, rel string) {
	t.Helper()
	full := filepath.Join(base, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	content := `<?xml version="1.0"?>
<testsuite name="test" tests="1" failures="0" errors="0" time="0.1">
  <testcase name="Test" classname="test" time="0.1"/>
</testsuite>`
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))
}
