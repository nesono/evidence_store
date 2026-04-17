package watch

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nesono/evidence-store/adapters/bazel/internal/client"
	"github.com/nesono/evidence-store/adapters/bazel/internal/gitinfo"
	"github.com/nesono/evidence-store/adapters/bazel/internal/junitxml"
	"github.com/nesono/evidence-store/adapters/bazel/internal/testlogs"
)

// Watcher polls the bazel-testlogs directory and uploads new test results.
type Watcher struct {
	workspaceDir string
	testlogsDir  string
	cfg          Config
	logger       *slog.Logger
}

// NewWatcher creates a watcher for the given workspace directory.
func NewWatcher(workspaceDir string, cfg Config, logger *slog.Logger) (*Watcher, error) {
	testlogsDir, err := resolveTestlogsDir(workspaceDir)
	if err != nil {
		return nil, fmt.Errorf("resolve testlogs dir: %w", err)
	}

	return &Watcher{
		workspaceDir: workspaceDir,
		testlogsDir:  testlogsDir,
		cfg:          cfg,
		logger:       logger,
	}, nil
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	w.logger.Info("watcher started",
		"workspace", w.workspaceDir,
		"testlogs", w.testlogsDir,
		"poll_interval", w.cfg.PollInterval,
		"api_url", w.cfg.APIURL,
	)

	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("watcher stopped")
			return nil
		case <-ticker.C:
			if err := w.poll(ctx); err != nil {
				w.logger.Error("poll cycle failed", "error", err)
			}
		}
	}
}

// poll runs a single check-and-upload cycle.
func (w *Watcher) poll(ctx context.Context) error {
	// Scan testlogs directory for test.xml files.
	entries, err := testlogs.Scan(w.testlogsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // testlogs dir doesn't exist yet — no tests run
		}
		return fmt.Errorf("scan testlogs: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}

	// Load the snapshot to see what we've already uploaded.
	snap, err := LoadSnapshot(w.workspaceDir)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}

	// Find test.xml files that changed since last upload.
	var allPaths []string
	for _, e := range entries {
		allPaths = append(allPaths, e.XMLPath)
	}
	changedPaths, err := snap.ChangedFiles(allPaths)
	if err != nil {
		return fmt.Errorf("check changed files: %w", err)
	}
	if len(changedPaths) == 0 {
		return nil
	}

	w.logger.Info("detected new test results", "count", len(changedPaths))

	// Wait for Bazel to finish (lock file released).
	if err := w.waitForBazelIdle(ctx); err != nil {
		return fmt.Errorf("wait for bazel: %w", err)
	}

	// Record snapshot time BEFORE reading files.
	snapshotTime := time.Now().UTC()

	// Build a set of changed paths for quick lookup.
	changedSet := make(map[string]struct{}, len(changedPaths))
	for _, p := range changedPaths {
		changedSet[p] = struct{}{}
	}

	// Filter entries to only changed ones, and read test.xml data into memory.
	var changedEntries []testlogs.TestLogEntry
	type parsedResult struct {
		entry    testlogs.TestLogEntry
		result   string
		duration float64
	}
	var results []parsedResult

	for _, entry := range entries {
		if _, ok := changedSet[entry.XMLPath]; !ok {
			continue
		}
		changedEntries = append(changedEntries, entry)

		result, duration, err := w.parseEntry(entry)
		if err != nil {
			w.logger.Warn("skipping unparseable entry", "target", entry.BazelTarget, "error", err)
			continue
		}
		results = append(results, parsedResult{entry: entry, result: result, duration: duration})
	}

	if len(results) == 0 {
		return nil
	}

	// Auto-detect git info.
	info, err := gitinfo.Detect()
	if err != nil {
		w.logger.Warn("could not detect git info", "error", err)
		return nil // can't upload without repo/ref
	}
	source := gitinfo.DetectSource()

	// Build evidence records.
	records := make([]client.EvidenceRecord, 0, len(results))
	for _, r := range results {
		metadata := map[string]any{
			"duration_s":        r.duration,
			"result_was_cached": r.entry.WasCached,
		}
		if len(w.cfg.Tags) > 0 {
			metadata["tags"] = w.cfg.Tags
		}

		records = append(records, client.EvidenceRecord{
			Repo:         info.Repo,
			Branch:       info.Branch,
			RCSRef:       info.Ref,
			ProcedureRef: r.entry.BazelTarget,
			EvidenceType: "bazel",
			Source:       source,
			Result:       r.result,
			FinishedAt:   snapshotTime.Format(time.RFC3339),
			Metadata:     metadata,
		})
	}

	// Upload.
	if w.cfg.APIURL == "" {
		w.logger.Warn("api_url not configured, skipping upload")
		return nil
	}

	var opts []client.Option
	if w.cfg.APIKey != "" {
		opts = append(opts, client.WithAPIKey(w.cfg.APIKey))
	}
	c := client.New(w.cfg.APIURL, opts...)

	uploadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	responses, err := c.PostBatch(uploadCtx, records)
	if err != nil {
		w.logger.Error("upload failed", "error", err)
		return nil // don't fail the poll cycle — try again next time
	}

	var created, failed int
	for _, resp := range responses {
		for _, r := range resp.Results {
			if r.Status == "created" {
				created++
			} else {
				failed++
			}
		}
	}
	w.logger.Info("upload complete", "created", created, "failed", failed)

	// Record the uploaded files in the snapshot.
	var uploadedPaths []string
	for _, r := range results {
		uploadedPaths = append(uploadedPaths, r.entry.XMLPath)
	}
	snap.Record(uploadedPaths)
	snap.UploadedAt = snapshotTime
	if err := snap.Save(w.workspaceDir); err != nil {
		w.logger.Error("failed to save snapshot", "error", err)
	}

	return nil
}

// parseEntry reads and parses a single test entry, returning result and duration.
func (w *Watcher) parseEntry(entry testlogs.TestLogEntry) (string, float64, error) {
	f, err := os.Open(entry.XMLPath)
	if err != nil {
		return "", 0, fmt.Errorf("open %s: %w", entry.XMLPath, err)
	}
	defer f.Close()

	ts, err := junitxml.Parse(f)
	if err != nil {
		return "", 0, fmt.Errorf("parse %s: %w", entry.XMLPath, err)
	}

	if ts != nil {
		result, duration := junitxml.AggregateResult(ts)
		return result, duration, nil
	}

	// Empty XML stub — try test.log.
	if result, ok := testlogs.ResultFromLog(entry.LogPath); ok {
		return result, 0, nil
	}

	return "", 0, fmt.Errorf("empty test.xml and unparseable test.log for %s", entry.BazelTarget)
}

// waitForBazelIdle waits until the Bazel workspace lock is released.
// Uses the output_base/lock file as a signal that Bazel is running.
func (w *Watcher) waitForBazelIdle(ctx context.Context) error {
	lockPath, err := w.bazelLockPath()
	if err != nil {
		// Can't determine lock path — just wait the debounce time and hope.
		w.logger.Warn("cannot determine bazel lock path, using debounce wait", "error", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(w.cfg.DebounceWait):
			return nil
		}
	}

	w.logger.Debug("waiting for bazel lock release", "lock", lockPath)

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !isFileLocked(lockPath) {
				return nil
			}
		}
	}
}

// bazelLockPath returns the path to the Bazel output base lock file.
func (w *Watcher) bazelLockPath() (string, error) {
	cmd := exec.Command("bazel", "info", "output_base")
	cmd.Dir = w.workspaceDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("bazel info output_base: %w", err)
	}
	return filepath.Join(strings.TrimSpace(string(out)), "lock"), nil
}

// isFileLocked is implemented in flock_unix.go / flock_windows.go.

// resolveTestlogsDir finds the testlogs directory for the workspace.
func resolveTestlogsDir(workspaceDir string) (string, error) {
	// First try "bazel info bazel-testlogs".
	cmd := exec.Command("bazel", "info", "bazel-testlogs")
	cmd.Dir = workspaceDir
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	// Fall back to the conventional symlink.
	testlogsDir := filepath.Join(workspaceDir, "bazel-testlogs")
	if _, err := os.Lstat(testlogsDir); err == nil {
		return testlogsDir, nil
	}

	return testlogsDir, nil // return default path even if it doesn't exist yet
}
