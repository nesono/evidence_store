package tests

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

// ---------------------------------------------------------------------------
// Tests: Manual Test Logs
// ---------------------------------------------------------------------------

// A manual test's log lives in `metadata.observations` (DESIGN.md §2.2) and is
// markdown written by hand. Nothing on the write path may reflow, trim or
// re-encode it: the UI renders exactly what came back, so a lost newline is a
// list that stops being a list.
const testLog = "## Run 2\n" +
	"\n" +
	"1. Powered on the rig — all lights green\n" +
	"2. Pressed the **brake pedal** — soft, 20mm travel\n" +
	"\n" +
	"```\n" +
	"ERR_42: pressure low\n" +
	"```\n" +
	"\n" +
	"> Photos: https://example.com/runs/42\n"

func TestTestLogRoundTripsVerbatim(t *testing.T) {
	repo := "org/testlog_" + uuid.New().String()[:8]

	body := map[string]any{
		"repo":          repo,
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "manual/brake-check",
		"evidence_type": "manual",
		"source":        "j.tester",
		"result":        "FAIL",
		"finished_at":   "2026-03-30 14:00",
		"metadata": map[string]any{
			"tags":         []string{"manual"},
			"notes":        "pedal soft on run 2",
			"observations": testLog,
		},
	}

	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := decodeJSON[model.Evidence](t, resp)

	resp = getJSON(t, "/api/v1/evidence/"+created.ID.String())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	fetched := decodeJSON[model.Evidence](t, resp)

	var meta struct {
		Notes        string `json:"notes"`
		Observations string `json:"observations"`
	}
	require.NoError(t, json.Unmarshal(fetched.Metadata, &meta))

	assert.Equal(t, testLog, meta.Observations)
	// Notes and the log are separate fields, not two names for one thing: a
	// record's summary survives the log being long.
	assert.Equal(t, "pedal soft on run 2", meta.Notes)
}

// The log is free text, so a tester quoting HTML or writing a script tag into
// their observations must be stored as written — escaping is the renderer's
// job, and doing it here too would corrupt the record.
func TestTestLogKeepsMarkupAsWritten(t *testing.T) {
	repo := "org/testlog_html_" + uuid.New().String()[:8]
	log := "saw <script>alert(1)</script> printed on screen 2 & the rig beeped"

	body := map[string]any{
		"repo":          repo,
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "manual/screen-check",
		"evidence_type": "manual",
		"source":        "j.tester",
		"result":        "PASS",
		"finished_at":   "2026-03-30 14:00",
		"metadata":      map[string]any{"observations": log},
	}

	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := decodeJSON[model.Evidence](t, resp)

	var meta struct {
		Observations string `json:"observations"`
	}
	require.NoError(t, json.Unmarshal(created.Metadata, &meta))
	assert.Equal(t, log, meta.Observations)
}
