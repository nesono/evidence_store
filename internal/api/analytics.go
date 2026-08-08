package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/nesono/evidence-store/internal/analytics"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/store"
)

type AnalyticsHandler struct {
	evidence *store.EvidenceStore
	cfg      *config.Config
}

func NewAnalyticsHandler(es *store.EvidenceStore, cfg *config.Config) *AnalyticsHandler {
	return &AnalyticsHandler{evidence: es, cfg: cfg}
}

// analyticsWindow describes the span the numbers were computed over, so a
// caller can tell at a glance whether the filter selected what they meant.
type analyticsWindow struct {
	From  *time.Time `json:"from,omitempty"`
	To    *time.Time `json:"to,omitempty"`
	Runs  int        `json:"runs"`
	Tests int        `json:"tests"`
}

// Tests returns one row of reliability metrics per test.
//
// It accepts the full evidence filter vocabulary, plus the labelling thresholds,
// a sort key, and offset paging. Sorting and paging happen in memory: the rates
// are defined in Go, and computing them a second time in SQL just to order by
// them is how the two definitions would drift apart.
func (h *AnalyticsHandler) Tests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter, err := parseEvidenceFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	thresholds, err := parseThresholds(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sortKey := q.Get("sort")
	if sortKey != "" && !analytics.IsSortable(sortKey) {
		writeError(w, http.StatusBadRequest, "cannot sort by "+sortKey)
		return
	}

	desc := false
	switch order := q.Get("order"); order {
	case "", "asc":
	case "desc":
		desc = true
	default:
		writeError(w, http.StatusBadRequest, `order must be "asc" or "desc"`)
		return
	}

	limit := h.cfg.DefaultPageSize
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > h.cfg.MaxPageSize {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be between 1 and %d", h.cfg.MaxPageSize))
			return
		}
		limit = n
	}

	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = n
	}

	stats, err := h.evidence.TestStats(r.Context(), store.TestStatsParams{
		Filter:              filter,
		GroupByEvidenceType: q.Get("group_by") == "evidence_type",
	})
	if err != nil {
		var tooMany *store.ErrTooManyGroups
		if errors.As(err, &tooMany) {
			writeError(w, http.StatusUnprocessableEntity, tooMany.Error())
			return
		}
		slog.Error("failed to aggregate test stats", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	analytics.Finalize(stats, thresholds)

	// Labels are derived, not stored, so filtering by one happens here rather
	// than in SQL. This is the honest way to ask "which tests always fail":
	// ranking by fail rate only shows the worst, which on a healthy suite is
	// still nowhere near always.
	if label := q.Get("label"); label != "" {
		if !analytics.IsLabel(label) {
			writeError(w, http.StatusBadRequest, "unknown label "+label)
			return
		}
		stats = analytics.WithLabel(stats, label)
	}

	if err := analytics.Sort(stats, sortKey, desc); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// The window is derived from the rows already in hand rather than from a
	// second query, so it always describes exactly the set being reported.
	window := summarizeWindow(stats)

	page := stats
	if offset >= len(page) {
		page = nil
	} else {
		page = page[offset:]
	}
	if len(page) > limit {
		page = page[:limit]
	}
	if page == nil {
		page = []analytics.TestStats{}
	}

	writeJSON(w, http.StatusOK, struct {
		Window     analyticsWindow       `json:"window"`
		Thresholds analytics.Thresholds  `json:"thresholds"`
		Tests      []analytics.TestStats `json:"tests"`
		Total      int                   `json:"total"`
	}{
		Window:     window,
		Thresholds: thresholds,
		Tests:      page,
		Total:      len(stats),
	})
}

// Clustering caps. The matrix is held in memory and the pairwise similarity is
// quadratic in the number of tests, so both dimensions are bounded. Exceeding
// either is reported rather than truncated: a coverage percentage computed from
// part of the failures is worse than no answer, because it looks like an answer.
const (
	MaxClusterTests = 2000
	MaxClusterRuns  = 20000
)

// DefaultClusterThreshold is the Jaccard similarity at which two tests are
// considered to fail together.
const DefaultClusterThreshold = 0.6

// Clusters groups tests that fail together and returns the smallest set of
// tests that still catches most failing runs.
func (h *AnalyticsHandler) Clusters(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	filter, err := parseEvidenceFilter(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	runKey := store.RunKey(q.Get("run_key"))
	if runKey == "" {
		runKey = store.RunKeyAuto
	}
	if !runKey.Valid() {
		writeError(w, http.StatusBadRequest,
			`run_key must be one of "auto", "invocation", "commit"`)
		return
	}

	includeErrors := q.Get("include_errors") == "true"

	threshold := DefaultClusterThreshold
	if err := parseRateParam(q, "threshold", &threshold); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	occurrences, err := h.evidence.FailureOccurrences(r.Context(), store.FailureOccurrenceParams{
		Filter:        filter,
		RunKey:        runKey,
		IncludeErrors: includeErrors,
	})
	if err != nil {
		var tooMany *store.ErrTooManyRows
		if errors.As(err, &tooMany) {
			writeError(w, http.StatusUnprocessableEntity, tooMany.Error())
			return
		}
		slog.Error("failed to load failure occurrences", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	matrix := analytics.NewMatrix(occurrences)
	if matrix.Tests() > MaxClusterTests {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
			"query matches %d distinct failing tests, more than the %d that can be clustered; narrow the filter or the time window",
			matrix.Tests(), MaxClusterTests))
		return
	}
	if matrix.Runs() > MaxClusterRuns {
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
			"query matches %d failing runs, more than the %d that can be clustered; narrow the filter or the time window",
			matrix.Runs(), MaxClusterRuns))
		return
	}

	writeJSON(w, http.StatusOK, struct {
		RunKey        store.RunKey          `json:"run_key"`
		IncludeErrors bool                  `json:"include_errors"`
		Threshold     float64               `json:"threshold"`
		Tests         int                   `json:"tests"`
		FailingRuns   int                   `json:"failing_runs"`
		Clusters      []analytics.Cluster   `json:"clusters"`
		MinimalSet    []analytics.CoverStep `json:"minimal_set"`
	}{
		RunKey:        runKey,
		IncludeErrors: includeErrors,
		Threshold:     threshold,
		Tests:         matrix.Tests(),
		FailingRuns:   matrix.FailingRuns(),
		Clusters:      matrix.Cluster(threshold),
		MinimalSet:    matrix.GreedyCover(),
	})
}

// Summary returns the headline counts for a filter window.
func (h *AnalyticsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	filter, err := parseEvidenceFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sum, err := h.evidence.Summary(r.Context(), filter)
	if err != nil {
		slog.Error("failed to summarize evidence", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Rates come from the same definitions the per-test table uses.
	counts := analytics.Counts{
		Pass:    int(sum.Pass),
		Fail:    int(sum.Fail),
		Error:   int(sum.Error),
		Skipped: int(sum.Skipped),
	}

	writeJSON(w, http.StatusOK, struct {
		*store.WindowSummary
		FailRate  float64 `json:"fail_rate"`
		ErrorRate float64 `json:"error_rate"`
	}{
		WindowSummary: sum,
		FailRate:      counts.FailRate(),
		ErrorRate:     counts.ErrorRate(),
	})
}

func summarizeWindow(stats []analytics.TestStats) analyticsWindow {
	window := analyticsWindow{Tests: len(stats)}
	for _, s := range stats {
		window.Runs += s.Runs
		if window.From == nil || s.FirstSeen.Before(*window.From) {
			first := s.FirstSeen
			window.From = &first
		}
		if window.To == nil || s.LastSeen.After(*window.To) {
			last := s.LastSeen
			window.To = &last
		}
	}
	return window
}

// parseThresholds reads the labelling thresholds, falling back to the defaults.
// They are request parameters because what counts as "almost always failing" is
// a judgement about a particular suite, not a universal constant.
func parseThresholds(q url.Values) (analytics.Thresholds, error) {
	th := analytics.DefaultThresholds()

	if err := parseIntParam(q, "min_runs", &th.MinRuns, 0); err != nil {
		return th, err
	}
	if err := parseIntParam(q, "min_errors", &th.MinErrors, 0); err != nil {
		return th, err
	}
	if err := parseRateParam(q, "always_failing_rate", &th.AlwaysFailingRate); err != nil {
		return th, err
	}
	if err := parseRateParam(q, "flip_rate", &th.FlipRate); err != nil {
		return th, err
	}
	if err := parseRateParam(q, "error_rate", &th.ErrorRate); err != nil {
		return th, err
	}

	return th, nil
}

func parseIntParam(q url.Values, name string, target *int, min int) error {
	v := q.Get(name)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		return fmt.Errorf("%s must be an integer >= %d", name, min)
	}
	*target = n
	return nil
}

func parseRateParam(q url.Values, name string, target *float64) error {
	v := q.Get(name)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		return fmt.Errorf("%s must be a number between 0 and 1", name)
	}
	*target = f
	return nil
}
