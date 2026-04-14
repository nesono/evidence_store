package tests

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

// ---------------------------------------------------------------------------
// Tests: UTC Normalization
// ---------------------------------------------------------------------------

func TestCreateEvidenceWithTimezoneOffset(t *testing.T) {
	repo := "org/utc_offset_" + uuid.New().String()[:8]

	// POST with +02:00 offset — should be stored as UTC (12:00Z).
	body := map[string]string{
		"repo":          repo,
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "//pkg:test",
		"evidence_type": "manual",
		"source":        "tester",
		"result":        "PASS",
		"finished_at":   "2026-03-30T14:00:00+02:00",
	}
	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	result := decodeJSON[model.Evidence](t, resp)
	assert.Equal(t, "2026-03-30T12:00:00Z", result.FinishedAt.Format("2006-01-02T15:04:05Z07:00"))
}

func TestCreateEvidenceWithZonelessTime(t *testing.T) {
	repo := "org/utc_zoneless_" + uuid.New().String()[:8]

	// POST with zoneless datetime — should be treated as UTC.
	body := map[string]string{
		"repo":          repo,
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "//pkg:test",
		"evidence_type": "manual",
		"source":        "tester",
		"result":        "PASS",
		"finished_at":   "2026-03-30 14:00",
	}
	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	result := decodeJSON[model.Evidence](t, resp)
	assert.Equal(t, "2026-03-30T14:00:00Z", result.FinishedAt.Format("2006-01-02T15:04:05Z07:00"))
}

func TestCreateEvidenceWithDateOnly(t *testing.T) {
	repo := "org/utc_dateonly_" + uuid.New().String()[:8]

	body := map[string]string{
		"repo":          repo,
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "//pkg:test",
		"evidence_type": "manual",
		"source":        "tester",
		"result":        "PASS",
		"finished_at":   "2026-03-30",
	}
	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	result := decodeJSON[model.Evidence](t, resp)
	assert.Equal(t, "2026-03-30T00:00:00Z", result.FinishedAt.Format("2006-01-02T15:04:05Z07:00"))
}

func TestFilterWithFlexibleDateFormats(t *testing.T) {
	repo := "org/utc_filter_" + uuid.New().String()[:8]

	ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Filter with zoneless date — should be interpreted as UTC.
	resp = getJSON(t, "/api/v1/evidence?repo="+url.QueryEscape(repo)+"&finished_after="+url.QueryEscape("2020-01-01 00:00"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)

	// Filter with date-only.
	resp = getJSON(t, "/api/v1/evidence?repo="+url.QueryEscape(repo)+"&finished_after="+url.QueryEscape("2020-01-01"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result = decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)
}

func TestResponseTimestampsAreUTC(t *testing.T) {
	repo := "org/utc_resp_" + uuid.New().String()[:8]

	ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	result := decodeJSON[model.Evidence](t, resp)
	assert.Equal(t, "UTC", result.FinishedAt.Location().String())
	assert.Equal(t, "UTC", result.IngestedAt.Location().String())
}
