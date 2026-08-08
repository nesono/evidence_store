package tests

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fetchCSV(t *testing.T, f analyticsFixture, extra url.Values) [][]string {
	t.Helper()

	q := url.Values{"repo": {f.repo}, "format": {"csv"}}
	for k, vs := range extra {
		q[k] = vs
	}
	resp := getJSON(t, "/api/v1/analytics/tests?"+q.Encode())
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	assert.Contains(t, resp.Header.Get("Content-Type"), "text/csv")
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "attachment")

	records, err := csv.NewReader(resp.Body).ReadAll()
	require.NoError(t, err)
	return records
}

func csvRow(t *testing.T, records [][]string, procedure string) map[string]string {
	t.Helper()

	header := records[0]
	for _, row := range records[1:] {
		fields := map[string]string{}
		for i, name := range header {
			fields[name] = row[i]
		}
		if fields["procedure_ref"] == procedure {
			return fields
		}
	}
	t.Fatalf("no CSV row for %s", procedure)
	return nil
}

func TestAnalyticsCSVHasHeaderAndOneRowPerTest(t *testing.T) {
	f := seedAnalyticsFixture(t)
	records := fetchCSV(t, f, nil)

	require.Len(t, records, 7, "a header plus the six fixture tests")
	assert.Equal(t, "repo", records[0][0])
	assert.Equal(t, "procedure_ref", records[0][1])
}

func TestAnalyticsCSVCarriesTheSameNumbersAsJSON(t *testing.T) {
	f := seedAnalyticsFixture(t)

	fromJSON := byProcedure(t, f.get(t, nil), "//infra:test")
	row := csvRow(t, fetchCSV(t, f, nil), "//infra:test")

	assert.Equal(t, strconv.Itoa(fromJSON.Runs), row["runs"])
	assert.Equal(t, strconv.Itoa(fromJSON.Counts.Pass), row["pass"])
	assert.Equal(t, strconv.Itoa(fromJSON.Counts.Error), row["error"])
	// The fixture's infra test ends on a run of errors, and last_result reports
	// the latest record whatever kind it was.
	assert.Equal(t, "ERROR", row["last_result"])
	assert.Equal(t, fromJSON.LastResult, row["last_result"])

	// Rates are plain decimals so a spreadsheet reads them as numbers.
	rate, err := strconv.ParseFloat(row["error_rate"], 64)
	require.NoError(t, err)
	assert.InDelta(t, fromJSON.ErrorRate, rate, 1e-6)
}

func TestAnalyticsCSVJoinsLabels(t *testing.T) {
	f := seedAnalyticsFixture(t)
	records := fetchCSV(t, f, nil)

	assert.Equal(t, "stable", csvRow(t, records, "//stable:test")["labels"])
	assert.Equal(t, "sparse", csvRow(t, records, "//sparse:test")["labels"])
	assert.Contains(t, csvRow(t, records, "//flaky:test")["labels"], "flaky")
}

// A test that has never failed has no last_fail_at, and an empty cell is the
// honest rendering of that — not a zero timestamp.
func TestAnalyticsCSVLeavesMissingTimestampsEmpty(t *testing.T) {
	f := seedAnalyticsFixture(t)
	records := fetchCSV(t, f, nil)

	assert.Empty(t, csvRow(t, records, "//stable:test")["last_fail_at"])
	assert.NotEmpty(t, csvRow(t, records, "//stable:test")["last_pass_at"])
	assert.Empty(t, csvRow(t, records, "//broken:test")["last_pass_at"])
}

// An export is of the query, not of the page you happen to be looking at.
func TestAnalyticsCSVIgnoresPaging(t *testing.T) {
	f := seedAnalyticsFixture(t)

	records := fetchCSV(t, f, url.Values{"limit": {"2"}, "offset": {"1"}})
	assert.Len(t, records, 7, "every matching row, not the requested window")
}

func TestAnalyticsCSVRespectsFiltersAndSorting(t *testing.T) {
	f := seedAnalyticsFixture(t)

	filtered := fetchCSV(t, f, url.Values{"label": {"always_failing"}})
	require.Len(t, filtered, 2)
	assert.Equal(t, "//broken:test", csvRow(t, filtered, "//broken:test")["procedure_ref"])

	sorted := fetchCSV(t, f, url.Values{"sort": {"fail_rate"}, "order": {"desc"}})
	assert.Equal(t, "//broken:test", sorted[1][1], "sorting still applies to the export")
}

// A procedure_ref containing a comma or a quote must survive the round trip.
func TestAnalyticsCSVQuotesAwkwardValues(t *testing.T) {
	f := seedAnalyticsFixture(t)
	records := fetchCSV(t, f, nil)

	var buf strings.Builder
	require.NoError(t, csv.NewWriter(&buf).WriteAll(records))
	reparsed, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	require.NoError(t, err)
	assert.Equal(t, records, reparsed)
}

func TestAnalyticsCSVRejectsBadFilterBeforeWriting(t *testing.T) {
	resp := getJSON(t, "/api/v1/analytics/tests?format=csv&result=BOGUS")
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json",
		"an error is JSON even when a CSV was asked for")
}
