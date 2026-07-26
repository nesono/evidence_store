package tests

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

// seedRepo inserts n records under a unique repo and returns the repo name.
func seedRepo(t *testing.T, prefix string, n int) string {
	t.Helper()
	repo := "org/" + prefix + "_" + uuid.New().String()[:8]
	for i := range n {
		ev := makeEvidence(repo, "main", "off123", fmt.Sprintf("//pkg:test_%d", i), "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}
	return repo
}

// ---------------------------------------------------------------------------
// Tests: Offset pagination
// ---------------------------------------------------------------------------

func TestListEvidenceOffsetWalksSameOrderAsCursor(t *testing.T) {
	repo := seedRepo(t, "offset_walk", 6)

	// Walk the whole set with cursors.
	var cursorIDs []uuid.UUID
	var cursor *string
	for {
		url := fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2", repo)
		if cursor != nil {
			url += "&cursor=" + *cursor
		}
		resp := getJSON(t, url)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		page := decodeJSON[listResponse](t, resp)
		for _, r := range page.Records {
			cursorIDs = append(cursorIDs, r.ID)
		}
		if page.NextCursor == nil {
			break
		}
		cursor = page.NextCursor
	}
	require.Len(t, cursorIDs, 6)

	// Walk the same set with offsets.
	var offsetIDs []uuid.UUID
	for off := 0; off < 6; off += 2 {
		resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2&offset=%d", repo, off))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		page := decodeJSON[listResponse](t, resp)
		assert.Len(t, page.Records, 2, "offset %d should return a full window", off)
		for _, r := range page.Records {
			offsetIDs = append(offsetIDs, r.ID)
		}
	}

	assert.Equal(t, cursorIDs, offsetIDs, "offset walk must visit records in the same order as the cursor walk")
}

func TestListEvidenceOffsetPastEndReturnsEmpty(t *testing.T) {
	repo := seedRepo(t, "offset_past_end", 3)

	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=10&offset=100", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	page := decodeJSON[listResponse](t, resp)
	assert.Empty(t, page.Records)
	assert.Nil(t, page.NextCursor)
	require.NotNil(t, page.Total, "total is still reported past the end so the UI can correct itself")
	assert.Equal(t, int64(3), *page.Total)
}

func TestListEvidenceOffsetStillReturnsTotal(t *testing.T) {
	repo := seedRepo(t, "offset_total", 5)

	// A deep-linked window (offset > 0) must still report the total, otherwise the
	// UI cannot render "3-5 of 5" on first load.
	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2&offset=2", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	page := decodeJSON[listResponse](t, resp)
	assert.Len(t, page.Records, 2)
	require.NotNil(t, page.Total)
	assert.Equal(t, int64(5), *page.Total)
}

func TestListEvidenceIncludeTotalFalseOmitsTotal(t *testing.T) {
	repo := seedRepo(t, "offset_no_total", 4)

	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2&include_total=false", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	page := decodeJSON[listResponse](t, resp)
	assert.Len(t, page.Records, 2)
	assert.Nil(t, page.Total, "include_total=false must skip the count")
}

func TestListEvidenceOffsetWithCursorRejected(t *testing.T) {
	repo := seedRepo(t, "offset_cursor_clash", 3)

	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=1", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	page := decodeJSON[listResponse](t, resp)
	require.NotNil(t, page.NextCursor)

	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=1&offset=1&cursor=%s", repo, *page.NextCursor))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"offset and cursor are different pagination modes and must not be combined")
}

func TestListEvidenceInvalidOffsetRejected(t *testing.T) {
	for _, offset := range []string{"-1", "abc", "1.5"} {
		t.Run(offset, func(t *testing.T) {
			resp := getJSON(t, "/api/v1/evidence?offset="+offset)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Sorting
// ---------------------------------------------------------------------------

// seedSorted inserts records with distinct finished_at values and procedure refs.
func seedSorted(t *testing.T, prefix string) (string, []string) {
	t.Helper()
	repo := "org/" + prefix + "_" + uuid.New().String()[:8]
	base := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)
	procedures := []string{"//pkg:c_test", "//pkg:a_test", "//pkg:b_test"}
	for i, proc := range procedures {
		ev := makeEvidence(repo, "main", "srt123", proc, "ci", model.ResultPass)
		// Descending finished_at, so it disagrees with insertion order.
		ev.FinishedAt = model.FlexibleTime{Time: base.Add(time.Duration(-i) * time.Hour)}
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}
	return repo, procedures
}

func TestListEvidenceSortByProcedureRef(t *testing.T) {
	repo, _ := seedSorted(t, "sort_proc")

	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&sort=procedure_ref", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	page := decodeJSON[listResponse](t, resp)
	require.Len(t, page.Records, 3)
	assert.Equal(t, []string{"//pkg:a_test", "//pkg:b_test", "//pkg:c_test"},
		[]string{page.Records[0].ProcedureRef, page.Records[1].ProcedureRef, page.Records[2].ProcedureRef})

	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&sort=procedure_ref&order=desc", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	page = decodeJSON[listResponse](t, resp)
	require.Len(t, page.Records, 3)
	assert.Equal(t, []string{"//pkg:c_test", "//pkg:b_test", "//pkg:a_test"},
		[]string{page.Records[0].ProcedureRef, page.Records[1].ProcedureRef, page.Records[2].ProcedureRef})
}

func TestListEvidenceSortByFinishedAt(t *testing.T) {
	repo, _ := seedSorted(t, "sort_finished")

	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&sort=finished_at&order=desc", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)

	page := decodeJSON[listResponse](t, resp)
	require.Len(t, page.Records, 3)
	for i := 1; i < len(page.Records); i++ {
		assert.False(t, page.Records[i].FinishedAt.After(page.Records[i-1].FinishedAt),
			"records must be ordered by finished_at descending")
	}
}

func TestListEvidenceSortAcceptsAllWhitelistedColumns(t *testing.T) {
	repo := seedRepo(t, "sort_columns", 2)

	columns := []string{
		"repo", "branch", "rcs_ref", "procedure_ref",
		"evidence_type", "source", "result", "finished_at", "ingested_at",
	}
	for _, col := range columns {
		t.Run(col, func(t *testing.T) {
			resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&sort=%s", repo, col))
			defer resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)
		})
	}
}

func TestListEvidenceSortRejectsUnknownColumn(t *testing.T) {
	// An un-whitelisted column must be refused rather than interpolated into SQL.
	for _, sort := range []string{"metadata", "id; DROP TABLE evidence", "unknown_column"} {
		t.Run(sort, func(t *testing.T) {
			resp := getJSON(t, "/api/v1/evidence?sort="+url.QueryEscape(sort))
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
		})
	}
}

func TestListEvidenceSortRejectsInvalidOrder(t *testing.T) {
	resp := getJSON(t, "/api/v1/evidence?sort=repo&order=sideways")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestListEvidenceSortWithCursorRejected(t *testing.T) {
	repo := seedRepo(t, "sort_cursor_clash", 3)

	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=1", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	page := decodeJSON[listResponse](t, resp)
	require.NotNil(t, page.NextCursor)

	// Keyset pagination is tied to (ingested_at, id); re-sorting would silently
	// skip or repeat records.
	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=1&sort=repo&cursor=%s", repo, *page.NextCursor))
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestListEvidenceSortIsStableAcrossWindows(t *testing.T) {
	// All six records share the same repo, so sorting by `repo` is entirely ties.
	// Without an id tie-break, windows could repeat or skip records.
	repo := seedRepo(t, "sort_stable", 6)

	seen := make(map[uuid.UUID]bool)
	for off := 0; off < 6; off += 2 {
		resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2&offset=%d&sort=repo", repo, off))
		require.Equal(t, http.StatusOK, resp.StatusCode)
		page := decodeJSON[listResponse](t, resp)
		require.Len(t, page.Records, 2)
		for _, r := range page.Records {
			assert.False(t, seen[r.ID], "record repeated across windows")
			seen[r.ID] = true
		}
	}
	assert.Len(t, seen, 6, "every record must appear exactly once across the windows")
}

// ---------------------------------------------------------------------------
// Tests: Inherited records are kept out of the window
// ---------------------------------------------------------------------------

func TestInheritedRecordsReturnedSeparately(t *testing.T) {
	repo := "org/inh_separate_" + uuid.New().String()[:8]

	// Two records on the source commit, one directly on the target commit.
	for i := range 2 {
		ev := makeEvidence(repo, "main", "src_sep", fmt.Sprintf("//pkg:inh_%d", i), "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}
	ev := makeEvidence(repo, "main", "tgt_sep", "//pkg:direct", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo:          repo,
		SourceRCSRef:  "src_sep",
		TargetRCSRef:  "tgt_sep",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "no changes",
		CreatedBy:     "ci",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&rcs_ref=tgt_sep&limit=10", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	page := decodeJSON[listResponse](t, resp)

	// `records` holds only the directly-matching record, so the window range and
	// total stay consistent; inherited ones live in their own field.
	require.Len(t, page.Records, 1)
	assert.Equal(t, "//pkg:direct", page.Records[0].ProcedureRef)
	assert.False(t, *page.Records[0].Inherited)
	require.NotNil(t, page.Total)
	assert.Equal(t, int64(1), *page.Total, "total counts the window's records, not inherited ones")

	require.Len(t, page.InheritedRecords, 2)
	for _, r := range page.InheritedRecords {
		assert.True(t, *r.Inherited)
		assert.NotNil(t, r.InheritanceDeclaration)
	}
}

func TestInheritedRecordsNeverExceedTheWindow(t *testing.T) {
	repo := "org/inh_window_" + uuid.New().String()[:8]

	// Five inheritable records on the source commit, two on the target.
	for i := range 5 {
		ev := makeEvidence(repo, "main", "src_win", fmt.Sprintf("//pkg:src_%d", i), "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}
	for i := range 2 {
		ev := makeEvidence(repo, "main", "tgt_win", fmt.Sprintf("//pkg:tgt_%d", i), "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo:          repo,
		SourceRCSRef:  "src_win",
		TargetRCSRef:  "tgt_win",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "no changes",
		CreatedBy:     "ci",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&rcs_ref=tgt_win&limit=2", repo))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	page := decodeJSON[listResponse](t, resp)

	assert.LessOrEqual(t, len(page.Records), 2, "records must never exceed the requested limit")
	assert.Len(t, page.InheritedRecords, 5)
}
