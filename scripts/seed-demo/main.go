// Command seed-demo fills an Evidence Store database with synthetic records for
// demos and manual testing of the web UI.
//
// It writes to Postgres directly with COPY rather than going through the API.
// The batch endpoint inserts one row per round trip (store.InsertBatch), which
// at two million records takes tens of minutes; COPY takes a couple of minutes.
// The trade-off is that API-level validation is bypassed, so the generator below
// is written to satisfy validate.EvidenceCreate on its own.
//
// Usage:
//
//	go run ./scripts/seed-demo                      # 2,000,000 records
//	go run ./scripts/seed-demo --count 50000        # a smaller set
//	go run ./scripts/seed-demo --truncate           # replace existing evidence
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	var (
		databaseURL = flag.String("database-url", envOrDefault("EVIDENCE_DATABASE_URL",
			"postgres://evidence:evidence@localhost:5432/evidence_store?sslmode=disable"), "Postgres connection string")
		count    = flag.Int("count", 2_000_000, "Number of records to generate")
		batch    = flag.Int("batch", 50_000, "Rows per COPY batch")
		days     = flag.Int("days", 180, "Spread finished_at over this many days back from now")
		repos    = flag.Int("repos", 12, "Number of distinct repositories to generate")
		seed     = flag.Int64("seed", 1, "Random seed; the same seed produces the same data")
		truncate = flag.Bool("truncate", false, "Delete all existing evidence first")
	)
	flag.Parse()

	if *count <= 0 || *batch <= 0 || *days <= 0 || *repos <= 0 {
		fmt.Fprintln(os.Stderr, "error: --count, --batch, --days and --repos must all be positive")
		os.Exit(1)
	}

	// Ctrl-C stops cleanly between batches; rows already copied stay committed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *databaseURL, *count, *batch, *days, *repos, *seed, *truncate); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURL string, count, batch, days, repoCount int, seed int64, truncate bool) error {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("parse database url: %w", err)
	}
	// `result` is a Postgres enum. COPY uses the binary protocol, so pgx needs the
	// type registered on each connection before it can encode a Go string into it.
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		t, err := c.LoadType(ctx, "evidence_result")
		if err != nil {
			return fmt.Errorf("load evidence_result enum: %w", err)
		}
		c.TypeMap().RegisterType(t)
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	var existing int64
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM evidence").Scan(&existing); err != nil {
		return fmt.Errorf("count existing evidence: %w", err)
	}

	if truncate {
		fmt.Printf("deleting %s existing record(s)...\n", humanize(existing))
		if _, err := pool.Exec(ctx, "TRUNCATE evidence"); err != nil {
			return fmt.Errorf("truncate evidence: %w", err)
		}
		existing = 0
	} else if existing > 0 {
		fmt.Printf("note: %s record(s) already present; adding to them (use --truncate to replace)\n", humanize(existing))
	}

	g := newGenerator(seed, days, repoCount)

	fmt.Printf("seeding %s records into %s\n", humanize(int64(count)), redactPassword(databaseURL))
	start := time.Now()
	written := 0

	for written < count {
		n := min(batch, count-written)

		rows, err := pool.CopyFrom(ctx,
			pgx.Identifier{"evidence"},
			[]string{"repo", "branch", "rcs_ref", "procedure_ref", "evidence_type", "source", "result", "finished_at", "ingested_at", "metadata"},
			pgx.CopyFromSlice(n, func(int) ([]any, error) { return g.row(), nil }),
		)
		if err != nil {
			if ctx.Err() != nil {
				fmt.Printf("\ninterrupted after %s records\n", humanize(int64(written)))
				return nil
			}
			return fmt.Errorf("copy batch: %w", err)
		}
		written += int(rows)

		elapsed := time.Since(start)
		rate := float64(written) / elapsed.Seconds()
		eta := time.Duration(float64(count-written)/rate) * time.Second
		fmt.Printf("\r  %s / %s (%.0f rows/s, eta %s)      ",
			humanize(int64(written)), humanize(int64(count)), rate, eta.Round(time.Second))
	}
	fmt.Println()

	// Without fresh statistics the planner mis-estimates the COUNT(*) and sorts
	// the UI issues, which makes a freshly seeded demo look slower than it is.
	fmt.Print("analyzing table... ")
	if _, err := pool.Exec(ctx, "ANALYZE evidence"); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	fmt.Println("done")

	fmt.Printf("\nseeded %s records in %s\n", humanize(int64(written)), time.Since(start).Round(time.Second))
	return summarize(ctx, pool)
}

// summarize prints what was generated so the demo driver knows what to filter on.
func summarize(ctx context.Context, pool *pgxpool.Pool) error {
	var total int64
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM evidence").Scan(&total); err != nil {
		return fmt.Errorf("count evidence: %w", err)
	}
	fmt.Printf("\ntable now holds %s records\n", humanize(total))

	rows, err := pool.Query(ctx, `
		SELECT repo, COUNT(*) FROM evidence GROUP BY repo ORDER BY COUNT(*) DESC LIMIT 5
	`)
	if err != nil {
		return fmt.Errorf("summarize repos: %w", err)
	}
	defer rows.Close()

	fmt.Println("\ntop repositories:")
	for rows.Next() {
		var repo string
		var n int64
		if err := rows.Scan(&repo, &n); err != nil {
			return err
		}
		fmt.Printf("  %-32s %s\n", repo, humanize(n))
	}
	return rows.Err()
}

// ---------------------------------------------------------------------------
// Generator
// ---------------------------------------------------------------------------

// generator produces records that look like real CI output: mostly passing,
// clustered onto a limited set of repos, branches and commits so that filtering
// in the UI returns meaningful groups rather than one row per value.
type generator struct {
	rnd      *rand.Rand
	repos    []string
	commits  [][]string // per repo, so a commit filter selects one repo's run
	now      time.Time
	spread   time.Duration
	branches []string
	types    []string
	sources  []string
	packages []string
	targets  []string
	tags     []string
}

func newGenerator(seed int64, days, repoCount int) *generator {
	rnd := rand.New(rand.NewSource(seed))

	orgs := []string{"nesono", "acme", "globex", "initech"}
	names := []string{
		"evidence-store", "payments-api", "web-frontend", "data-pipeline",
		"auth-service", "mobile-app", "infra-tooling", "search-index",
		"billing-worker", "notification-hub", "reporting", "gateway",
	}

	g := &generator{
		rnd:    rnd,
		now:    time.Now().UTC(),
		spread: time.Duration(days) * 24 * time.Hour,
		branches: []string{
			"main", "main", "main", "main", "main", "main", "main",
			"develop", "develop",
			"release/2.4", "release/2.5",
			"feature/checkout-v2", "feature/oauth-refresh", "feature/dark-mode",
		},
		// Must satisfy validate.EvidenceCreate's ^[a-z][a-z0-9_]{0,63}$ pattern.
		types: []string{"bazel", "bazel", "bazel", "bazel", "pytest", "gotest", "junit", "manual"},
		sources: []string{
			"https://ci.example.com/build/4711", "https://ci.example.com/build/4712",
			"https://github.com/nesono/evidence_store/actions/runs/30207857523",
			"jenkins-agent-03", "buildkite-agent-11", "alice", "bob", "carol",
		},
		packages: []string{
			"//internal/api", "//internal/store", "//internal/auth", "//internal/model",
			"//internal/retention", "//internal/validate", "//internal/ratelimit",
			"//cmd/server", "//web", "//adapters/bazel/internal/watch", "//tests",
		},
		targets: []string{
			"unit_test", "integration_test", "smoke_test", "regression_test",
			"golden_test", "e2e_test", "contract_test", "fuzz_test",
		},
		tags: []string{"ci", "nightly", "regression", "smoke", "flaky", "slow", "critical", "local", "dev"},
	}

	for i := range repoCount {
		g.repos = append(g.repos, fmt.Sprintf("%s/%s",
			orgs[i%len(orgs)], names[i%len(names)]))

		// Each repo gets a pool of commits, so filtering by one commit returns a
		// realistic single build's worth of results rather than a lone record.
		commits := make([]string, 40)
		for j := range commits {
			commits[j] = randomHex(rnd, 40)
		}
		g.commits = append(g.commits, commits)
	}

	return g
}

func (g *generator) row() []any {
	repoIdx := g.rnd.Intn(len(g.repos))
	repo := g.repos[repoIdx]
	commit := g.commits[repoIdx][g.rnd.Intn(len(g.commits[repoIdx]))]

	result := g.result()

	// Bias timestamps towards the recent past, the way an active repo's history
	// looks: dense near today, thinning out further back.
	age := time.Duration(g.rnd.Float64() * g.rnd.Float64() * float64(g.spread))
	finishedAt := g.now.Add(-age)
	// Results are ingested shortly after the run finishes. The default list
	// ordering is by ingested_at, so this keeps that ordering meaningful.
	ingestedAt := finishedAt.Add(time.Duration(g.rnd.Intn(600)+5) * time.Second)

	procedure := fmt.Sprintf("%s:%s",
		g.packages[g.rnd.Intn(len(g.packages))],
		g.targets[g.rnd.Intn(len(g.targets))])

	evidenceType := g.types[g.rnd.Intn(len(g.types))]

	return []any{
		repo,
		g.branches[g.rnd.Intn(len(g.branches))],
		commit,
		procedure,
		evidenceType,
		g.sources[g.rnd.Intn(len(g.sources))],
		result,
		finishedAt,
		ingestedAt,
		g.metadata(evidenceType, result, procedure),
	}
}

// result returns a realistic verdict distribution: mostly green, with enough
// failures to make the result filter worth using.
func (g *generator) result() string {
	switch n := g.rnd.Intn(100); {
	case n < 88:
		return "PASS"
	case n < 95:
		return "FAIL"
	case n < 98:
		return "ERROR"
	default:
		return "SKIPPED"
	}
}

func (g *generator) metadata(evidenceType, result, procedure string) []byte {
	meta := map[string]any{
		"tags":        g.pickTags(),
		"duration_ms": g.rnd.Intn(120_000) + 40,
	}

	// Only manual runs carry a test log — a machine has nothing to observe. The
	// demo set needs some so the record dialog's log viewer has something to show.
	if evidenceType == "manual" {
		meta["observations"] = g.observations(result, procedure)
	}

	switch result {
	case "FAIL":
		meta["notes"] = fmt.Sprintf("assertion failed in %s: expected 200, got %d",
			procedure, []int{400, 403, 404, 409, 500, 503}[g.rnd.Intn(6)])
	case "ERROR":
		meta["notes"] = []string{
			"test binary crashed: signal SIGSEGV",
			"timed out after 300s",
			"could not connect to test database",
			"out of memory",
		}[g.rnd.Intn(4)]
	case "SKIPPED":
		meta["notes"] = "skipped: requires docker"
	}

	b, err := json.Marshal(meta)
	if err != nil {
		// The map holds only strings, ints and []string, so this cannot fail.
		panic(err)
	}
	return b
}

// observations writes the kind of markdown a tester leaves behind: numbered
// steps, a pasted error, a link to the run — enough shape to show what the
// record dialog does with a log.
func (g *generator) observations(result, procedure string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n", procedure)
	fmt.Fprintf(&b, "Tester: %s, rig %d.\n\n", g.sources[g.rnd.Intn(len(g.sources))], g.rnd.Intn(8)+1)
	b.WriteString("1. Powered on the rig — all lights green\n")
	b.WriteString("2. Ran the procedure end to end\n")
	b.WriteString("3. Logged the readings\n\n")

	switch result {
	case "PASS":
		b.WriteString("No deviations from the expected behaviour.\n")
	case "FAIL":
		fmt.Fprintf(&b, "Step 3 deviated:\n\n```\nexpected 200, got %d\n```\n",
			[]int{400, 403, 404, 409, 500, 503}[g.rnd.Intn(6)])
	case "ERROR":
		b.WriteString("> Rig dropped the connection mid-run; result is not trustworthy.\n")
	case "SKIPPED":
		b.WriteString("Could not run: the hardware was booked by another team.\n")
	}

	fmt.Fprintf(&b, "\nPhotos: https://photos.example.com/runs/%d\n", g.rnd.Intn(9000)+1000)
	return b.String()
}

func (g *generator) pickTags() []string {
	n := g.rnd.Intn(3) + 1
	picked := make([]string, 0, n)
	for range n {
		t := g.tags[g.rnd.Intn(len(g.tags))]
		if !contains(picked, t) {
			picked = append(picked, t)
		}
	}
	return picked
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const hexDigits = "0123456789abcdef"

func randomHex(rnd *rand.Rand, n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = hexDigits[rnd.Intn(len(hexDigits))]
	}
	return string(b)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// humanize formats n with thousands separators, matching how the UI reads.
func humanize(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return s + "," + strings.Join(parts, ",")
}

// redactPassword strips credentials so the connection string can be printed.
func redactPassword(url string) string {
	at := strings.LastIndex(url, "@")
	slashes := strings.Index(url, "//")
	if at == -1 || slashes == -1 || at < slashes {
		return url
	}
	return url[:slashes+2] + "***@" + url[at+1:]
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
