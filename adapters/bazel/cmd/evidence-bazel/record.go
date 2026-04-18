package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nesono/evidence-store/adapters/bazel/internal/client"
	"github.com/nesono/evidence-store/adapters/bazel/internal/gitinfo"
	"github.com/nesono/evidence-store/adapters/bazel/internal/watch"
)

// findWorkspaceDir returns the directory containing .evidence/config.yaml,
// searching upward from BUILD_WORKSPACE_DIRECTORY (when set) or cwd. Returns
// empty string when no config file is found.
func findWorkspaceDir() string {
	start := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		start = cwd
	}
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".evidence", "config.yaml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

type recordOptions struct {
	APIURL       string
	APIKey       string
	Repo         string
	Branch       string
	RCSRef       string
	Source       string
	ProcedureRef string
	EvidenceType string
	Result       string
	Notes        string
	Tags         string
	DurationMS   int64
	Metadata     string
	InvocationID string
	FinishedAt   string
	DryRun       bool
}

// buildRecord validates options and constructs a single EvidenceRecord.
// The caller is responsible for populating Repo/Branch/RCSRef (typically via
// gitinfo.Detect).
func buildRecord(opts recordOptions) (client.EvidenceRecord, error) {
	if opts.ProcedureRef == "" {
		return client.EvidenceRecord{}, fmt.Errorf("--procedure-ref is required")
	}
	if opts.Repo == "" {
		return client.EvidenceRecord{}, fmt.Errorf("--repo is required (could not auto-detect)")
	}
	if opts.RCSRef == "" {
		return client.EvidenceRecord{}, fmt.Errorf("--rcs-ref is required (could not auto-detect)")
	}

	result := strings.ToUpper(strings.TrimSpace(opts.Result))
	switch result {
	case "PASS", "FAIL", "ERROR", "SKIPPED":
	default:
		return client.EvidenceRecord{}, fmt.Errorf("--result must be one of PASS, FAIL, ERROR, SKIPPED (got %q)", opts.Result)
	}

	finishedAt := opts.FinishedAt
	if finishedAt == "" {
		finishedAt = time.Now().UTC().Format(time.RFC3339)
	}

	metadata := map[string]any{}
	if opts.Metadata != "" {
		if err := json.Unmarshal([]byte(opts.Metadata), &metadata); err != nil {
			return client.EvidenceRecord{}, fmt.Errorf("--metadata must be a JSON object: %w", err)
		}
	}
	if opts.Notes != "" {
		metadata["notes"] = opts.Notes
	}
	if tags := parseTags(opts.Tags); len(tags) > 0 {
		metadata["tags"] = tags
	}
	if opts.DurationMS > 0 {
		metadata["duration_ms"] = opts.DurationMS
	}
	if opts.InvocationID != "" {
		metadata["invocation_id"] = opts.InvocationID
	}

	evidenceType := opts.EvidenceType
	if evidenceType == "" {
		evidenceType = "bazel-manual"
	}

	rec := client.EvidenceRecord{
		Repo:         opts.Repo,
		Branch:       opts.Branch,
		RCSRef:       opts.RCSRef,
		ProcedureRef: opts.ProcedureRef,
		EvidenceType: evidenceType,
		Source:       opts.Source,
		Result:       result,
		FinishedAt:   finishedAt,
	}
	if len(metadata) > 0 {
		rec.Metadata = metadata
	}
	return rec, nil
}

func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func writeRecord(w io.Writer, rec client.EvidenceRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}

func runRecord(args []string) {
	// Resolve config defaults: .evidence/config.yaml (if present) → env vars.
	// Command-line flags override both via flag.Parse.
	var cfgAPIURL, cfgAPIKey, cfgTags string
	if wd := findWorkspaceDir(); wd != "" {
		cfg, err := watch.LoadConfig(wd)
		if err != nil {
			slog.Warn("failed to load .evidence/config.yaml", "dir", wd, "error", err)
		} else {
			cfgAPIURL = cfg.APIURL
			cfgAPIKey = cfg.APIKey
			if len(cfg.Tags) > 0 {
				cfgTags = strings.Join(cfg.Tags, ",")
			}
		}
	} else {
		// No config file; fall back to env vars directly.
		cfgAPIURL = os.Getenv("EVIDENCE_STORE_URL")
		cfgAPIKey = os.Getenv("EVIDENCE_STORE_API_KEY")
	}

	fs := flag.NewFlagSet("record", flag.ExitOnError)

	apiURL := fs.String("api-url", cfgAPIURL, "Evidence Store API base URL (required unless --dry-run)")
	apiKey := fs.String("api-key", cfgAPIKey, "API key for authentication (optional)")
	repo := fs.String("repo", "", "Repository identifier (auto-detected from git remote)")
	branch := fs.String("branch", "", "Branch name (auto-detected from git)")
	rcsRef := fs.String("rcs-ref", "", "RCS reference / commit hash (auto-detected from git HEAD)")
	source := fs.String("source", "", "Source identifier: CI build URL or username (auto-detected)")
	procedureRef := fs.String("procedure-ref", "", "Procedure / test identifier (required)")
	evidenceType := fs.String("evidence-type", "bazel-manual", "Evidence type (e.g. bazel-failure-test)")
	result := fs.String("result", "", "Result: PASS, FAIL, ERROR, or SKIPPED (required)")
	notes := fs.String("notes", "", "Free-text context stored under metadata.notes")
	tags := fs.String("tags", cfgTags, "Comma-separated tags stored under metadata.tags")
	durationMS := fs.Int64("duration-ms", 0, "Duration in milliseconds (optional)")
	metadataJSON := fs.String("metadata", "", "JSON object of arbitrary metadata to merge")
	invocationID := fs.String("invocation-id", "", "Bazel invocation ID (optional)")
	finishedAt := fs.String("finished-at", "", "Finish time (RFC3339). Defaults to now (UTC)")
	dryRun := fs.Bool("dry-run", false, "Print record to stdout instead of posting")

	_ = fs.Parse(args)

	if *repo == "" || *branch == "" || *rcsRef == "" {
		info, err := gitinfo.Detect()
		if err != nil {
			slog.Warn("could not auto-detect git info", "error", err)
		} else {
			if *repo == "" {
				*repo = info.Repo
			}
			if *branch == "" {
				*branch = info.Branch
			}
			if *rcsRef == "" {
				*rcsRef = info.Ref
			}
		}
	}
	if *source == "" {
		*source = gitinfo.DetectSource()
	}

	opts := recordOptions{
		APIURL:       *apiURL,
		APIKey:       *apiKey,
		Repo:         *repo,
		Branch:       *branch,
		RCSRef:       *rcsRef,
		Source:       *source,
		ProcedureRef: *procedureRef,
		EvidenceType: *evidenceType,
		Result:       *result,
		Notes:        *notes,
		Tags:         *tags,
		DurationMS:   *durationMS,
		Metadata:     *metadataJSON,
		InvocationID: *invocationID,
		FinishedAt:   *finishedAt,
		DryRun:       *dryRun,
	}

	rec, err := buildRecord(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if opts.DryRun {
		if err := writeRecord(os.Stdout, rec); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if opts.APIURL == "" {
		fmt.Fprintln(os.Stderr, "error: --api-url is required (or set EVIDENCE_STORE_URL)")
		os.Exit(1)
	}

	var clientOpts []client.Option
	if opts.APIKey != "" {
		clientOpts = append(clientOpts, client.WithAPIKey(opts.APIKey))
	}
	c := client.New(opts.APIURL, clientOpts...)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	responses, err := c.PostBatch(ctx, []client.EvidenceRecord{rec})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error posting evidence: %v\n", err)
		os.Exit(1)
	}
	for _, resp := range responses {
		for _, r := range resp.Results {
			if r.Status != "created" {
				fmt.Fprintf(os.Stderr, "record failed: %s\n", r.Error)
				os.Exit(1)
			}
		}
	}
	slog.Info("record posted", "procedure_ref", rec.ProcedureRef, "result", rec.Result)
}
