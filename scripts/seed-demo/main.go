// Command seed-demo fills an Evidence Store database with synthetic records for
// demos and manual testing of the web UI.
//
// It writes to Postgres directly with COPY rather than going through the API.
// The batch endpoint inserts one row per round trip (store.InsertBatch), which
// at two million records takes tens of minutes; COPY takes a couple of minutes.
// The trade-off is that API-level validation is bypassed, so the generator below
// is written to satisfy validate.EvidenceCreate on its own.
//
// Manual tests are seeded separately from the bulk CI noise: a fixed-size batch
// of manual_test records with real images in the blob store, markdown logs of
// varying shape, and an exact verdict split, so the UI has something realistic
// to browse rather than just scale to page through.
//
// Usage:
//
//	go run ./scripts/seed-demo                      # 2,000,000 records
//	go run ./scripts/seed-demo --count 50000        # a smaller set
//	go run ./scripts/seed-demo --manual-tests 500   # fewer manual test logs
//	go run ./scripts/seed-demo --truncate           # replace existing evidence
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math/rand"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/config"
)

func main() {
	var (
		databaseURL = flag.String("database-url", envOrDefault("EVIDENCE_DATABASE_URL",
			"postgres://evidence:evidence@localhost:5432/evidence_store?sslmode=disable"), "Postgres connection string")
		count       = flag.Int("count", 2_000_000, "Number of records to generate")
		batch       = flag.Int("batch", 50_000, "Rows per COPY batch")
		days        = flag.Int("days", 180, "Spread finished_at over this many days back from now")
		repos       = flag.Int("repos", 12, "Number of distinct repositories to generate")
		seed        = flag.Int64("seed", 1, "Random seed; the same seed produces the same data")
		truncate    = flag.Bool("truncate", false, "Delete all existing evidence first")
		manualTests = flag.Int("manual-tests", 3000, "Number of manual_test records to seed, with real images in the blob store")
	)
	flag.Parse()

	if *count <= 0 || *batch <= 0 || *days <= 0 || *repos <= 0 {
		fmt.Fprintln(os.Stderr, "error: --count, --batch, --days and --repos must all be positive")
		os.Exit(1)
	}
	if *manualTests < 0 {
		fmt.Fprintln(os.Stderr, "error: --manual-tests must not be negative")
		os.Exit(1)
	}

	// Ctrl-C stops cleanly between batches; rows already copied stay committed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *databaseURL, *count, *batch, *days, *repos, *seed, *truncate, *manualTests); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, databaseURL string, count, batch, days, repoCount int, seed int64, truncate bool, manualTests int) error {
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
		// CASCADE because blob_ref references evidence: a reference to a record
		// that no longer exists is not worth keeping, and without this the
		// truncate fails outright.
		if _, err := pool.Exec(ctx, "TRUNCATE evidence CASCADE"); err != nil {
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

	if manualTests > 0 {
		if err := seedManualTests(ctx, pool, g, manualTests); err != nil {
			return fmt.Errorf("seed manual tests: %w", err)
		}
	}

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
	rnd        *rand.Rand
	repos      []string
	commits    [][]string // per repo, so a commit filter selects one repo's run
	now        time.Time
	spread     time.Duration
	branches   []string
	collectors []string
	sources    []string
	packages   []string
	targets    []string
	tags       []string
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
		// Which runner produced a CI record. The type says only that a machine
		// ran it (see evidenceType); this is the split migration 000006 made in
		// the real data, so the demo set has the same shape.
		collectors: []string{"bazel", "bazel", "bazel", "bazel", "pytest", "gotest", "junit"},
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

	evidenceType := g.evidenceType()

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

// evidenceType returns how a record was collected. Most evidence in a real
// store comes off a pipeline; a demonstration is rare enough that the demo set
// should show what a handful of them look like among millions of runs.
//
// manual_test is deliberately absent here: it is seeded separately by
// seedManualTests, as a curated batch with real images and a fixed verdict
// split rather than noise at bulk-COPY scale.
func (g *generator) evidenceType() string {
	switch n := g.rnd.Intn(100); {
	case n < 96:
		return "ci"
	default:
		return "demonstration"
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

	// A machine has nothing to observe, so it names the runner instead; a person
	// writes down what they saw. The demo set needs both: one so the type filter
	// has something behind it, the other so the record dialog's log viewer does.
	if evidenceType == "ci" {
		meta["collector"] = g.collectors[g.rnd.Intn(len(g.collectors))]
	} else {
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
// Manual tests
//
// Bulk CI noise is generated above by COPYing straight into evidence, which is
// why it fakes a photo as a bare URL instead of a real blob reference: nothing
// downstream reads it. Manual tests are what a tester actually browses in the
// record dialog, so this batch goes through the real content-addressed store
// (internal/blob) and writes matching blob_ref rows itself, since COPY skips
// the annotateBlobRefs step store.Insert would normally do.
// ---------------------------------------------------------------------------

// manualImage is a synthetic screenshot already written to the blob store,
// ready to be referenced from a log.
type manualImage struct {
	ref  blob.Ref
	size int64
}

// buildImagePool renders a handful of synthetic "screenshots" at a spread of
// resolutions and stores each once. Reusing the pool across records — rather
// than generating a fresh image per record — means most manual tests end up
// sharing objects, which is the dedup behaviour DESIGN.md 4.4 calls out and is
// worth demonstrating rather than hiding.
func buildImagePool(ctx context.Context, store blob.Store, rnd *rand.Rand) ([]manualImage, error) {
	type dims struct{ w, h int }
	sizes := []dims{
		{320, 180}, {480, 270}, {640, 360}, {960, 540},
		{1280, 720}, {1600, 900}, {1920, 1080},
	}

	pool := make([]manualImage, 0, 18)
	for range 18 {
		d := sizes[rnd.Intn(len(sizes))]
		data, err := renderScreenshot(rnd, d.w, d.h)
		if err != nil {
			return nil, err
		}
		digest, size, err := store.Put(ctx, bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("store demo image: %w", err)
		}
		pool = append(pool, manualImage{ref: blob.Ref{Digest: digest, Ext: "png"}, size: size})
	}
	return pool, nil
}

// renderScreenshot draws a synthetic UI mockup — a header bar and a scatter of
// coloured panels — rather than using any real photo, so the seed data can
// never carry a copyright question. Light per-pixel jitter keeps PNG
// compression from collapsing every image to the same handful of bytes, which
// is what gives the CAS a realistic spread of object sizes.
func renderScreenshot(rnd *rand.Rand, w, h int) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, w, h))

	bg := color.RGBA{uint8(24 + rnd.Intn(40)), uint8(24 + rnd.Intn(40)), uint8(28 + rnd.Intn(50)), 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	headerH := h / 12
	header := color.RGBA{uint8(210 + rnd.Intn(45)), uint8(210 + rnd.Intn(45)), uint8(210 + rnd.Intn(45)), 255}
	draw.Draw(img, image.Rect(0, 0, w, headerH), &image.Uniform{header}, image.Point{}, draw.Src)

	panels := 6 + rnd.Intn(10)
	for range panels {
		pw, ph := 20+rnd.Intn(max(w/4, 1)), 12+rnd.Intn(max(h/6, 1))
		px, py := rnd.Intn(max(w-pw, 1)), headerH+rnd.Intn(max(h-headerH-ph, 1))
		c := color.RGBA{uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), uint8(rnd.Intn(256)), 255}
		draw.Draw(img, image.Rect(px, py, px+pw, py+ph), &image.Uniform{c}, image.Point{}, draw.Src)
	}

	for range (w * h) / 40 {
		x, y := rnd.Intn(w), rnd.Intn(h)
		r, gr, b, a := img.At(x, y).RGBA()
		jitter := func(v uint32) uint8 {
			n := int(v>>8) + rnd.Intn(21) - 10
			return uint8(min(max(n, 0), 255))
		}
		img.Set(x, y, color.RGBA{jitter(r), jitter(gr), jitter(b), uint8(a >> 8)})
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode demo image: %w", err)
	}
	return buf.Bytes(), nil
}

// manualResultCounts splits n across the AC's fixed verdict distribution
// (50% pass, 20% skip, 20% error, 10% fail) using largest-remainder rounding,
// so the total is exactly n even when n does not divide evenly.
func manualResultCounts(n int) []string {
	weights := []struct {
		result string
		pct    float64
	}{
		{"PASS", 0.50},
		{"SKIPPED", 0.20},
		{"ERROR", 0.20},
		{"FAIL", 0.10},
	}

	counts := make([]int, len(weights))
	remainders := make([]float64, len(weights))
	assigned := 0
	for i, w := range weights {
		exact := w.pct * float64(n)
		counts[i] = int(exact)
		remainders[i] = exact - float64(counts[i])
		assigned += counts[i]
	}
	for remaining := n - assigned; remaining > 0; remaining-- {
		best := 0
		for i := 1; i < len(remainders); i++ {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		counts[best]++
		remainders[best] = -1
	}

	results := make([]string, 0, n)
	for i, w := range weights {
		for range counts[i] {
			results = append(results, w.result)
		}
	}
	return results
}

// manualSteps returns the numbered procedure a tester followed, drawn from a
// shared pool so runs vary without needing a step generator per procedure.
func (g *generator) manualSteps(long bool) []string {
	pool := []string{
		"Powered on the rig and confirmed all status lights were green",
		"Connected the diagnostic harness and verified the link came up",
		"Loaded the test procedure and confirmed the revision matched the DUT",
		"Ran the procedure end to end at nominal settings",
		"Logged the readings from the primary sensor",
		"Cross-checked the readings against the reference instrument",
		"Cycled power and repeated the measurement for consistency",
		"Captured a screenshot of the final readout",
		"Reset the rig to its default configuration",
		"Filed the raw log alongside this record",
	}
	n := 2 + g.rnd.Intn(2)
	if long {
		n = 5 + g.rnd.Intn(4)
	}
	idx := g.rnd.Perm(len(pool))
	picked := make([]string, 0, n)
	for i := 0; i < n && i < len(idx); i++ {
		picked = append(picked, pool[idx[i]])
	}
	return picked
}

// manualObservations writes the markdown a tester leaves behind: numbered
// steps, sometimes an environment table, embedded screenshots, and a
// result-specific narrative. long and images vary independently so the demo
// set has short logs and long ones, with and without pictures, rather than one
// shape repeated three thousand times.
func (g *generator) manualObservations(result, procedure string, images []manualImage, long bool) (string, []string) {
	var b strings.Builder

	fmt.Fprintf(&b, "## %s\n\n", procedure)
	tester := g.sources[g.rnd.Intn(len(g.sources))]
	fmt.Fprintf(&b, "**Tester:** %s  \n**Rig:** bench-%d\n\n", tester, g.rnd.Intn(8)+1)

	b.WriteString("### Steps\n\n")
	for i, step := range g.manualSteps(long) {
		fmt.Fprintf(&b, "%d. %s\n", i+1, step)
	}
	b.WriteString("\n")

	// A bullet list rather than a table: the log renderer's subset deliberately
	// stops short of tables (web/static/markdown.js), so this is the richest
	// structure a "realistic" log can actually lean on.
	if long {
		b.WriteString("### Environment\n\n")
		fmt.Fprintf(&b, "- **Firmware:** %d.%d.%d\n", g.rnd.Intn(4)+1, g.rnd.Intn(10), g.rnd.Intn(20))
		fmt.Fprintf(&b, "- **Ambient temp:** %d°C\n", 15+g.rnd.Intn(15))
		fmt.Fprintf(&b, "- **Humidity:** %d%%\n\n", 30+g.rnd.Intn(40))
	}

	var photoURIs []string
	if len(images) > 0 {
		b.WriteString("### Observations\n\n")
		for _, img := range images {
			path := img.ref.Path()
			fmt.Fprintf(&b, "![step capture](%s)\n\n", path)
			photoURIs = append(photoURIs, path)
		}
	} else {
		b.WriteString("### Observations\n\n")
	}

	switch result {
	case "PASS":
		b.WriteString("All steps completed as expected; no deviations observed.\n")
	case "FAIL":
		fmt.Fprintf(&b, "Deviation at the final step:\n\n```\nexpected a nominal reading, observed one out of range\n```\n")
	case "ERROR":
		b.WriteString("> Run aborted before completion: " + []string{
			"rig lost power mid-sequence",
			"instrument reported a fault code",
			"connection to the DUT dropped",
			"operator had to abort for safety",
		}[g.rnd.Intn(4)] + "\n")
	case "SKIPPED":
		b.WriteString("Not run: " + []string{
			"rig was booked by another team",
			"prerequisite hardware was unavailable",
			"procedure requires a calibration that is overdue",
		}[g.rnd.Intn(3)] + "\n")
	}

	return b.String(), photoURIs
}

// blobDestination describes where images are about to be written, so a run
// against the wrong backend is visible immediately rather than discovered
// later as a broken image in the UI. config.Load reads EVIDENCE_BLOB_* the
// same way cmd/server does, but nothing forces this script to be run with the
// variables a docker-compose deployment set for the server -- see the printed
// warning in seedManualTests.
func blobDestination(opts blob.Options) string {
	switch opts.Backend {
	case "", "fs":
		return fmt.Sprintf("the local fs backend at %q", opts.Path)
	case "s3":
		return fmt.Sprintf("the s3 backend (bucket %q at %s)", opts.S3.Bucket, opts.S3.Endpoint)
	default:
		return fmt.Sprintf("the %q backend", opts.Backend)
	}
}

// seedManualTests writes a curated batch of manual_test records: real images
// in the blob store, blob_ref rows for reachability, and an exact 50/20/20/10
// pass/skip/error/fail split (issue #81). It runs after the bulk COPY loop and
// uses its own explicit ids, since the reachability rows below need to name
// the same evidence_id the row was inserted with.
func seedManualTests(ctx context.Context, pool *pgxpool.Pool, g *generator, n int) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load blob config: %w", err)
	}
	store, err := blob.Open(ctx, cfg.Blob.Options)
	if err != nil {
		return fmt.Errorf("open blob store: %w", err)
	}

	fmt.Printf("\nseeding %s manual test record(s) with images in %s\n", humanize(int64(n)), blobDestination(cfg.Blob.Options))
	if cfg.Blob.Options.Backend == "" || cfg.Blob.Options.Backend == "fs" {
		fmt.Println("  note: this is the local-disk default, not necessarily where your running " +
			"server reads blobs from. If the server is behind docker-compose (EVIDENCE_BLOB_BACKEND=s3 " +
			"against MinIO), the images this writes will not be visible there -- rerun with matching " +
			"EVIDENCE_BLOB_* variables, e.g.:\n" +
			"    EVIDENCE_BLOB_BACKEND=s3 EVIDENCE_BLOB_S3_ENDPOINT=localhost:9000 \\\n" +
			"    EVIDENCE_BLOB_S3_BUCKET=evidence-blobs EVIDENCE_BLOB_S3_ACCESS_KEY=evidence \\\n" +
			"    EVIDENCE_BLOB_S3_SECRET_KEY=evidence-secret go run ./scripts/seed-demo ...")
	}

	images, err := buildImagePool(ctx, store, g.rnd)
	if err != nil {
		return err
	}

	results := manualResultCounts(n)
	g.rnd.Shuffle(len(results), func(i, j int) { results[i], results[j] = results[j], results[i] })

	type manualRow struct {
		id      uuid.UUID
		vals    []any
		digests []blob.Digest
	}
	rows := make([]manualRow, n)

	for i := range n {
		repoIdx := g.rnd.Intn(len(g.repos))
		repo := g.repos[repoIdx]
		commit := g.commits[repoIdx][g.rnd.Intn(len(g.commits[repoIdx]))]
		procedure := fmt.Sprintf("%s:%s",
			g.packages[g.rnd.Intn(len(g.packages))], g.targets[g.rnd.Intn(len(g.targets))])

		age := time.Duration(g.rnd.Float64() * g.rnd.Float64() * float64(g.spread))
		finishedAt := g.now.Add(-age)
		ingestedAt := finishedAt.Add(time.Duration(g.rnd.Intn(600)+5) * time.Second)

		result := results[i]
		long := g.rnd.Float64() < 0.45

		var picked []manualImage
		switch roll := g.rnd.Float64(); {
		case roll < 0.40:
			// no image
		case roll < 0.75:
			picked = []manualImage{images[g.rnd.Intn(len(images))]}
		case roll < 0.93:
			picked = []manualImage{images[g.rnd.Intn(len(images))], images[g.rnd.Intn(len(images))]}
		default:
			for range 3 {
				picked = append(picked, images[g.rnd.Intn(len(images))])
			}
		}

		observations, photoURIs := g.manualObservations(result, procedure, picked, long)

		meta := map[string]any{
			"tags":         g.pickTags(),
			"duration_ms":  g.rnd.Intn(120_000) + 40,
			"observations": observations,
		}
		if len(photoURIs) > 0 {
			meta["photo_uris"] = photoURIs
		}
		switch result {
		case "FAIL", "ERROR", "SKIPPED":
			meta["notes"] = "see observations"
		}

		metaJSON, err := json.Marshal(meta)
		if err != nil {
			return fmt.Errorf("marshal manual test metadata: %w", err)
		}

		id := uuid.New()
		rows[i] = manualRow{
			id: id,
			vals: []any{
				id, repo, g.branches[g.rnd.Intn(len(g.branches))], commit, procedure,
				"manual_test", g.sources[g.rnd.Intn(len(g.sources))], result,
				finishedAt, ingestedAt, metaJSON,
			},
		}

		seen := make(map[blob.Digest]bool, len(picked))
		for _, img := range picked {
			if !seen[img.ref.Digest] {
				seen[img.ref.Digest] = true
				rows[i].digests = append(rows[i].digests, img.ref.Digest)
			}
		}
	}

	_, err = pool.CopyFrom(ctx,
		pgx.Identifier{"evidence"},
		[]string{"id", "repo", "branch", "rcs_ref", "procedure_ref", "evidence_type", "source", "result", "finished_at", "ingested_at", "metadata"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) { return rows[i].vals, nil }),
	)
	if err != nil {
		return fmt.Errorf("copy manual test evidence: %w", err)
	}

	var refRows [][]any
	for _, r := range rows {
		for _, d := range r.digests {
			refRows = append(refRows, []any{string(d), r.id})
		}
	}
	if len(refRows) > 0 {
		_, err = pool.CopyFrom(ctx,
			pgx.Identifier{"blob_ref"},
			[]string{"digest", "evidence_id"},
			pgx.CopyFromSlice(len(refRows), func(i int) ([]any, error) { return refRows[i], nil }),
		)
		if err != nil {
			return fmt.Errorf("copy blob refs: %w", err)
		}
	}

	fmt.Printf("seeded %s manual test record(s) referencing %s image placement(s) across %s unique blob(s)\n",
		humanize(int64(n)), humanize(int64(len(refRows))), humanize(int64(len(images))))
	return nil
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
