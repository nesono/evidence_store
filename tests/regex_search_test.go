package tests

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

// ---------------------------------------------------------------------------
// Tests: Regex Search
// ---------------------------------------------------------------------------

func TestSearchRegexRepo(t *testing.T) {
	base := "org/regex_repo_" + uuid.New().String()[:8]
	repo1 := base + "_alpha"
	repo2 := base + "_beta"

	for _, repo := range []string{repo1, repo2} {
		ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	// Exact match — only repo1.
	resp := getJSON(t, "/api/v1/evidence?repo="+url.QueryEscape(repo1))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)

	// Regex match — both.
	resp = getJSON(t, "/api/v1/evidence?repo="+url.QueryEscape("~"+base+"_.*"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result = decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2)
}

func TestSearchRegexBranch(t *testing.T) {
	repo := "org/regex_branch_" + uuid.New().String()[:8]

	for _, branch := range []string{"release/v1.0", "release/v2.0", "feature/login"} {
		ev := makeEvidence(repo, branch, "ref1", "//pkg:test", "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&branch="+url.QueryEscape("~^release/.*"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2)
}

func TestSearchRegexProcedure(t *testing.T) {
	repo := "org/regex_proc_" + uuid.New().String()[:8]

	for _, proc := range []string{"//pkg/a:test1", "//pkg/a:test2", "//pkg/b:test1"} {
		ev := makeEvidence(repo, "main", "ref1", proc, "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	// Regex to match //pkg/a:* only.
	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&procedure_ref="+url.QueryEscape("~^//pkg/a:.*"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2)
}

func TestSearchRegexEvidenceType(t *testing.T) {
	repo := "org/regex_type_" + uuid.New().String()[:8]

	for _, etype := range []string{"bazel", "manual", "bazeltool"} {
		ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "ci", model.ResultPass)
		ev.EvidenceType = etype
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&evidence_type="+url.QueryEscape("~^bazel"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2) // bazel and bazeltool
}

func TestSearchRegexTags(t *testing.T) {
	repo := "org/regex_tags_" + uuid.New().String()[:8]

	ev1 := makeEvidence(repo, "main", "ref1", "//pkg:test1", "ci", model.ResultPass)
	ev1.Metadata = json.RawMessage(`{"tags": ["nightly-x86", "regression"]}`)
	resp := postJSON(t, "/api/v1/evidence", ev1)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	ev2 := makeEvidence(repo, "main", "ref1", "//pkg:test2", "ci", model.ResultPass)
	ev2.Metadata = json.RawMessage(`{"tags": ["nightly-arm64", "smoke"]}`)
	resp = postJSON(t, "/api/v1/evidence", ev2)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	ev3 := makeEvidence(repo, "main", "ref1", "//pkg:test3", "ci", model.ResultPass)
	ev3.Metadata = json.RawMessage(`{"tags": ["manual"]}`)
	resp = postJSON(t, "/api/v1/evidence", ev3)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Regex tag search for nightly-*.
	resp = getJSON(t, "/api/v1/evidence?repo="+repo+"&tags="+url.QueryEscape("~^nightly-"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2)
}

func TestSearchNotes(t *testing.T) {
	repo := "org/regex_notes_" + uuid.New().String()[:8]

	ev1 := makeEvidence(repo, "main", "ref1", "//pkg:test1", "ci", model.ResultPass)
	ev1.Metadata = json.RawMessage(`{"notes": "Tested on device XYZ-100"}`)
	resp := postJSON(t, "/api/v1/evidence", ev1)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	ev2 := makeEvidence(repo, "main", "ref1", "//pkg:test2", "ci", model.ResultPass)
	ev2.Metadata = json.RawMessage(`{"notes": "Tested on device ABC-200"}`)
	resp = postJSON(t, "/api/v1/evidence", ev2)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Exact notes match.
	resp = getJSON(t, "/api/v1/evidence?repo="+repo+"&notes="+url.QueryEscape("Tested on device XYZ-100"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)

	// Regex notes match.
	resp = getJSON(t, "/api/v1/evidence?repo="+repo+"&notes="+url.QueryEscape("~device.*-[12]00"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result = decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2)
}

func TestSearchExactStillWorks(t *testing.T) {
	repo := "org/regex_exact_" + uuid.New().String()[:8]

	ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Exact match should still work as before.
	resp = getJSON(t, "/api/v1/evidence?repo="+url.QueryEscape(repo)+"&branch=main")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)
}
