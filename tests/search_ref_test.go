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
// Tests: combined ref filter on the list endpoint
//
// The search UI offers one box for "branch, tag or commit", the same as
// analytics does, so the list endpoint has to answer that box on its own.
// ---------------------------------------------------------------------------

// seedSearchRefFixture inserts one record per identity shape and returns the
// repo they share, so a filter can be checked against a known set.
func seedSearchRefFixture(t *testing.T) string {
	t.Helper()

	repo := "org/search_ref_" + uuid.New().String()[:8]
	records := []model.EvidenceCreate{
		makeEvidence(repo, "main", "aaaa1111bbbb2222cccc3333dddd4444eeee5555", "//on:main", "ci", model.ResultPass),
		makeEvidence(repo, "release/1.1", "1111aaaa2222bbbb3333cccc4444dddd5555eeee", "//on:release11", "ci", model.ResultPass),
		makeEvidence(repo, "release/1.10", "9999aaaa8888bbbb7777cccc6666dddd5555eeee", "//on:release110", "ci", model.ResultPass),
		makeEvidence(repo, "feature/x", "v2.0.0", "//on:tag", "ci", model.ResultPass),
	}
	for _, ev := range records {
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}
	return repo
}

// searchRefProcedures lists the procedures a ref filter selects within a repo.
func searchRefProcedures(t *testing.T, repo, ref string) []string {
	t.Helper()

	q := url.Values{"repo": {repo}, "ref": {ref}}
	resp := getJSON(t, "/api/v1/evidence?"+q.Encode())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)

	out := make([]string, 0, len(result.Records))
	for _, r := range result.Records {
		out = append(out, r.ProcedureRef)
	}
	return out
}

func TestSearchRefMatchesBranch(t *testing.T) {
	repo := seedSearchRefFixture(t)
	assert.Equal(t, []string{"//on:main"}, searchRefProcedures(t, repo, "main"))
}

func TestSearchRefMatchesFullCommit(t *testing.T) {
	repo := seedSearchRefFixture(t)
	assert.Equal(t, []string{"//on:main"},
		searchRefProcedures(t, repo, "aaaa1111bbbb2222cccc3333dddd4444eeee5555"))
}

// Pasting an abbreviated SHA is the common case and has to find the full one.
func TestSearchRefMatchesAbbreviatedCommit(t *testing.T) {
	repo := seedSearchRefFixture(t)
	assert.Equal(t, []string{"//on:main"}, searchRefProcedures(t, repo, "aaaa111"))
}

func TestSearchRefMatchesTagStoredAsRef(t *testing.T) {
	repo := seedSearchRefFixture(t)
	assert.Equal(t, []string{"//on:tag"}, searchRefProcedures(t, repo, "v2.0.0"))
}

// Branches are matched whole. Prefix matching there would answer a request for
// release/1.1 with release/1.10 as well.
func TestSearchRefDoesNotPrefixMatchBranches(t *testing.T) {
	repo := seedSearchRefFixture(t)
	assert.Equal(t, []string{"//on:release11"}, searchRefProcedures(t, repo, "release/1.1"))
}

// The single box replaces two regex-capable fields, so it has to stay
// regex-capable itself — against both identity columns.
func TestSearchRefAcceptsRegex(t *testing.T) {
	repo := seedSearchRefFixture(t)
	assert.ElementsMatch(t,
		[]string{"//on:release11", "//on:release110"},
		searchRefProcedures(t, repo, "~^release/"))

	assert.ElementsMatch(t,
		[]string{"//on:tag"},
		searchRefProcedures(t, repo, "~^v2\\."))
}
