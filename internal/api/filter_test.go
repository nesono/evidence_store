package api

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	v, err := url.ParseQuery(raw)
	require.NoError(t, err)
	return v
}

func TestParseEvidenceFilterEmpty(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t, ""))
	require.NoError(t, err)
	assert.Equal(t, model.EvidenceFilter{}, f)
}

func TestParseEvidenceFilterTextFields(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t,
		"repo=org/repo&rcs_ref=abc123&branch=main&evidence_type=bazel&source=ci&procedure_ref=//pkg:test"))
	require.NoError(t, err)

	require.NotNil(t, f.Repo)
	assert.Equal(t, "org/repo", *f.Repo)
	require.NotNil(t, f.RCSRef)
	assert.Equal(t, "abc123", *f.RCSRef)
	require.NotNil(t, f.Branch)
	assert.Equal(t, "main", *f.Branch)
	require.NotNil(t, f.EvidenceType)
	assert.Equal(t, "bazel", *f.EvidenceType)
	require.NotNil(t, f.Source)
	assert.Equal(t, "ci", *f.Source)
	require.NotNil(t, f.ProcedureRef)
	assert.Equal(t, "//pkg:test", *f.ProcedureRef)
}

// The `~` regex and `*` prefix forms are interpreted in the store, so the parser
// must pass them through untouched rather than trying to understand them.
func TestParseEvidenceFilterPassesPatternsThrough(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t, "repo=~^org/.*&procedure_ref=//pkg/*"))
	require.NoError(t, err)

	require.NotNil(t, f.Repo)
	assert.Equal(t, "~^org/.*", *f.Repo)
	require.NotNil(t, f.ProcedureRef)
	assert.Equal(t, "//pkg/*", *f.ProcedureRef)
}

// An empty value means "not set", not "match the empty string".
func TestParseEvidenceFilterEmptyValuesAreUnset(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t, "repo=&branch=&notes="))
	require.NoError(t, err)

	assert.Nil(t, f.Repo)
	assert.Nil(t, f.Branch)
	assert.Nil(t, f.Notes)
}

func TestParseEvidenceFilterResults(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t, "result=PASS,FAIL"))
	require.NoError(t, err)
	assert.Equal(t, []model.EvidenceResult{model.ResultPass, model.ResultFail}, f.Result)
}

func TestParseEvidenceFilterResultsTrimsWhitespace(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t, "result=PASS,%20ERROR"))
	require.NoError(t, err)
	assert.Equal(t, []model.EvidenceResult{model.ResultPass, model.ResultError}, f.Result)
}

func TestParseEvidenceFilterRejectsUnknownResult(t *testing.T) {
	_, err := parseEvidenceFilter(mustQuery(t, "result=BOGUS"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BOGUS")
}

func TestParseEvidenceFilterTimes(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t,
		"finished_after=2026-01-01&finished_before=2026-12-31T23:59:00Z"))
	require.NoError(t, err)

	require.NotNil(t, f.FinishedAfter)
	assert.Equal(t, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), *f.FinishedAfter)
	require.NotNil(t, f.FinishedBefore)
	assert.Equal(t, time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC), *f.FinishedBefore)
}

func TestParseEvidenceFilterRejectsBadTime(t *testing.T) {
	_, err := parseEvidenceFilter(mustQuery(t, "finished_after=yesterday"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finished_after")

	_, err = parseEvidenceFilter(mustQuery(t, "finished_before=nope"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "finished_before")
}

func TestParseEvidenceFilterTagsAndNotes(t *testing.T) {
	f, err := parseEvidenceFilter(mustQuery(t, "tags=ci,nightly&notes=~flake"))
	require.NoError(t, err)

	assert.Equal(t, []string{"ci", "nightly"}, f.Tags)
	require.NotNil(t, f.Notes)
	assert.Equal(t, "~flake", *f.Notes)
}
