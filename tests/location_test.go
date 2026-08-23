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
// Tests: Location
// ---------------------------------------------------------------------------

// Where a manual test happened lives in `metadata.location` (DESIGN.md §2.2) as
// the text the tester wrote — a coordinate pair from the device, or a place
// name. The store must not read it: a value that came back rounded, reordered
// or turned into a number would be a claim about the place that the tester
// never made.

func locationOf(t *testing.T, raw json.RawMessage) (string, *float64) {
	t.Helper()
	var meta struct {
		Location  string   `json:"location"`
		AccuracyM *float64 `json:"location_accuracy_m"`
	}
	require.NoError(t, json.Unmarshal(raw, &meta))
	return meta.Location, meta.AccuracyM
}

func postLocated(t *testing.T, prefix, location string, extra map[string]any) model.Evidence {
	t.Helper()
	metadata := map[string]any{"location": location}
	for k, v := range extra {
		metadata[k] = v
	}

	body := map[string]any{
		"repo":          "org/" + prefix + "_" + uuid.New().String()[:8],
		"branch":        "main",
		"rcs_ref":       "abc123",
		"procedure_ref": "manual/brake-check",
		"evidence_type": "manual",
		"source":        "j.tester",
		"result":        "PASS",
		"finished_at":   "2026-03-30 14:00",
		"metadata":      metadata,
	}

	resp := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	return decodeJSON[model.Evidence](t, resp)
}

func TestLocationRoundTripsVerbatim(t *testing.T) {
	created := postLocated(t, "loc", "52.51631, 13.37771", nil)

	resp := getJSON(t, "/api/v1/evidence/"+created.ID.String())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	fetched := decodeJSON[model.Evidence](t, resp)

	location, accuracy := locationOf(t, fetched.Metadata)
	// A string, still — the trailing zero of "13.37770" is the receiver's
	// precision, and parsing the pair into floats would throw it away.
	assert.Equal(t, "52.51631, 13.37771", location)
	assert.Nil(t, accuracy)
}

// The same field takes an address, and an address is not a degenerate
// coordinate pair: no part of it is a number to be tidied.
func TestLocationKeepsAnAddressAsWritten(t *testing.T) {
	const address = "Prüfgelände Süd, Halle 2 — Bucht 4"
	created := postLocated(t, "loc_addr", address, nil)

	location, _ := locationOf(t, created.Metadata)
	assert.Equal(t, address, location)
}

// A fix good to three kilometres does not place a test at a bench. The margin
// is filed next to the point so a reader can tell the two apart later, when the
// tester is no longer there to ask.
func TestLocationCarriesItsAccuracy(t *testing.T) {
	created := postLocated(t, "loc_acc", "52.51631, 13.37771",
		map[string]any{"location_accuracy_m": 12.5})

	resp := getJSON(t, "/api/v1/evidence/"+created.ID.String())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	fetched := decodeJSON[model.Evidence](t, resp)

	location, accuracy := locationOf(t, fetched.Metadata)
	assert.Equal(t, "52.51631, 13.37771", location)
	require.NotNil(t, accuracy)
	assert.InDelta(t, 12.5, *accuracy, 0.0001)
}

// Location sits beside the tester's log rather than inside it, so a record can
// have either, both or neither, and adding one does not disturb the other.
func TestLocationAndTestLogAreIndependent(t *testing.T) {
	created := postLocated(t, "loc_log", "Lab 2, bay 4",
		map[string]any{"observations": testLog, "notes": "pedal soft on run 2"})

	var meta struct {
		Location     string `json:"location"`
		Observations string `json:"observations"`
		Notes        string `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(created.Metadata, &meta))
	assert.Equal(t, "Lab 2, bay 4", meta.Location)
	assert.Equal(t, testLog, meta.Observations)
	assert.Equal(t, "pedal soft on run 2", meta.Notes)
}
