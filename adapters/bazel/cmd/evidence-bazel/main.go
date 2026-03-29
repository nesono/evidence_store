package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/iss/evidence-store/adapters/bazel/internal/client"
	"github.com/iss/evidence-store/adapters/bazel/internal/gitinfo"
	"github.com/iss/evidence-store/adapters/bazel/internal/junitxml"
	"github.com/iss/evidence-store/adapters/bazel/internal/testlogs"
)

func main() {
	var (
		apiURL       = flag.String("api-url", envOrDefault("EVIDENCE_STORE_URL", ""), "Evidence Store API base URL (required)")
		repo         = flag.String("repo", "", "Repository identifier (auto-detected from git remote)")
		branch       = flag.String("branch", "", "Branch name (auto-detected from git)")
		rcsRef       = flag.String("rcs-ref", "", "RCS reference / commit hash (auto-detected from git HEAD)")
		source       = flag.String("source", "", "Source identifier: CI build URL or username (auto-detected)")
		testlogsDir  = flag.String("testlogs-dir", "bazel-testlogs", "Path to bazel-testlogs directory")
		invocationID = flag.String("invocation-id", "", "Bazel invocation ID (optional)")
		tags         = flag.String("tags", "", "Comma-separated tags (optional)")
		apiKey       = flag.String("api-key", envOrDefault("EVIDENCE_STORE_API_KEY", ""), "API key for authentication (optional)")
		dryRun       = flag.Bool("dry-run", false, "Print records to stdout instead of posting")
	)
	flag.Parse()

	// Auto-detect git info for unset flags.
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

	// Validate required fields.
	if !*dryRun && *apiURL == "" {
		fmt.Fprintln(os.Stderr, "error: --api-url is required (or set EVIDENCE_STORE_URL)")
		flag.Usage()
		os.Exit(1)
	}
	if *repo == "" {
		fmt.Fprintln(os.Stderr, "error: --repo is required (could not auto-detect)")
		os.Exit(1)
	}
	if *rcsRef == "" {
		fmt.Fprintln(os.Stderr, "error: --rcs-ref is required (could not auto-detect)")
		os.Exit(1)
	}

	// Scan testlogs.
	slog.Info("scanning testlogs", "dir", *testlogsDir)
	entries, err := testlogs.Scan(*testlogsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error scanning testlogs: %v\n", err)
		os.Exit(1)
	}
	if len(entries) == 0 {
		slog.Info("no test.xml files found")
		return
	}
	slog.Info("found test results", "count", len(entries))

	// Parse each test.xml and build evidence records.
	var records []client.EvidenceRecord
	var parseErrors int

	for _, entry := range entries {
		var result string
		var durationS float64

		f, err := os.Open(entry.XMLPath)
		if err != nil {
			slog.Error("failed to open test.xml", "path", entry.XMLPath, "error", err)
			parseErrors++
			continue
		}

		ts, err := junitxml.Parse(f)
		f.Close()
		if err != nil {
			slog.Error("failed to parse test.xml", "path", entry.XMLPath, "error", err)
			parseErrors++
			continue
		}

		if ts != nil {
			// XML had real test data.
			result, durationS = junitxml.AggregateResult(ts)
		} else {
			// Empty XML stub (e.g. rules_go) — fall back to test.log.
			if logResult, ok := testlogs.ResultFromLog(entry.LogPath); ok {
				result = logResult
			} else {
				slog.Warn("empty test.xml and could not parse test.log", "target", entry.BazelTarget)
				parseErrors++
				continue
			}
		}

		metadata := map[string]any{
			"duration_s":        durationS,
			"result_was_cached": entry.WasCached,
		}
		if *invocationID != "" {
			metadata["invocation_id"] = *invocationID
		}
		if *tags != "" {
			metadata["tags"] = strings.Split(*tags, ",")
		}

		records = append(records, client.EvidenceRecord{
			Repo:         *repo,
			Branch:       *branch,
			RCSRef:       *rcsRef,
			ProcedureRef: entry.BazelTarget,
			EvidenceType: "bazel",
			Source:       *source,
			Result:       result,
			FinishedAt:   time.Now().UTC().Format(time.RFC3339),
			Metadata:     metadata,
		})
	}

	slog.Info("built evidence records", "count", len(records), "parse_errors", parseErrors)

	// Dry run: print and exit.
	if *dryRun {
		for _, r := range records {
			fmt.Printf("%-50s %s\n", r.ProcedureRef, r.Result)
		}
		fmt.Printf("\nTotal: %d records (%d parse errors)\n", len(records), parseErrors)
		return
	}

	// Post to evidence store.
	var opts []client.Option
	if *apiKey != "" {
		opts = append(opts, client.WithAPIKey(*apiKey))
	}
	c := client.New(*apiURL, opts...)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	responses, err := c.PostBatch(ctx, records)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error posting evidence: %v\n", err)
		os.Exit(1)
	}

	// Summarize results.
	var created, failed int
	for _, resp := range responses {
		for _, r := range resp.Results {
			if r.Status == "created" {
				created++
			} else {
				failed++
				slog.Error("record failed", "index", r.Index, "error", r.Error)
			}
		}
	}

	slog.Info("upload complete", "created", created, "failed", failed, "parse_errors", parseErrors)
	if failed > 0 || parseErrors > 0 {
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
