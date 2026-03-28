package testlogs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type TestLogEntry struct {
	BazelTarget string // e.g., //pkg:my_test
	XMLPath     string // absolute path to test.xml
	WasCached   bool   // derived from test.cache_status sibling
}

// Scan walks testlogsDir and finds all test.xml files, mapping each to a Bazel target.
// Handles sharded tests (shard_N_of_M/test.xml) by returning one entry per shard.
func Scan(testlogsDir string) ([]TestLogEntry, error) {
	absDir, err := filepath.Abs(testlogsDir)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	var entries []TestLogEntry

	err = filepath.Walk(absDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || info.Name() != "test.xml" {
			return nil
		}

		rel, err := filepath.Rel(absDir, path)
		if err != nil {
			return fmt.Errorf("rel path: %w", err)
		}

		target, err := pathToTarget(rel)
		if err != nil {
			return nil // skip files we can't map
		}

		cached := checkCacheStatus(filepath.Dir(path))

		entries = append(entries, TestLogEntry{
			BazelTarget: target,
			XMLPath:     path,
			WasCached:   cached,
		})

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walk testlogs: %w", err)
	}

	return entries, nil
}

// pathToTarget converts a relative path under bazel-testlogs to a Bazel target label.
//
// Examples:
//
//	pkg/my_test/test.xml                         -> //pkg:my_test
//	foo/bar/baz_test/test.xml                    -> //foo/bar:baz_test
//	pkg/my_test/shard_1_of_2/test.xml            -> //pkg:my_test
func pathToTarget(rel string) (string, error) {
	parts := strings.Split(filepath.ToSlash(rel), "/")

	// Remove "test.xml" at the end.
	parts = parts[:len(parts)-1]

	// Remove shard directory if present (e.g., "shard_1_of_2").
	if len(parts) > 0 && strings.HasPrefix(parts[len(parts)-1], "shard_") {
		parts = parts[:len(parts)-1]
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("cannot derive target from path: %s", rel)
	}

	// Last component is the target name, rest is the package.
	targetName := parts[len(parts)-1]
	pkg := strings.Join(parts[:len(parts)-1], "/")

	if pkg == "" {
		return "//:" + targetName, nil
	}

	return fmt.Sprintf("//%s:%s", pkg, targetName), nil
}

// checkCacheStatus reads test.cache_status in dir and returns true if the result was cached.
func checkCacheStatus(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "test.cache_status"))
	if err != nil {
		return false
	}
	content := string(data)
	return strings.Contains(content, "REMOTE_CACHE_HIT") || strings.Contains(content, "locally cached")
}
