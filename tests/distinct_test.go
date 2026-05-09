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

type distinctResponse struct {
	Values []string `json:"values"`
}

func TestDistinctRepo(t *testing.T) {
	tag := uuid.New().String()[:8]
	repos := []string{"org/distinct_a_" + tag, "org/distinct_b_" + tag, "org/distinct_b_" + tag}

	for _, repo := range repos {
		ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence/distinct?field=repo&q="+url.QueryEscape(tag))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := decodeJSON[distinctResponse](t, resp)

	assert.ElementsMatch(t, []string{"org/distinct_a_" + tag, "org/distinct_b_" + tag}, got.Values)
}

func TestDistinctEvidenceType(t *testing.T) {
	repo := "org/distinct_etype_" + uuid.New().String()[:8]
	for _, etype := range []string{"manual", "ci", "manual"} {
		ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "src", model.ResultPass)
		ev.EvidenceType = etype
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence/distinct?field=evidence_type")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := decodeJSON[distinctResponse](t, resp)

	assert.Contains(t, got.Values, "manual")
	assert.Contains(t, got.Values, "ci")
}

func TestDistinctSourceWithQuery(t *testing.T) {
	tag := uuid.New().String()[:8]
	repo := "org/distinct_src_" + tag
	for _, src := range []string{"alice_" + tag, "bob_" + tag, "Alice2_" + tag} {
		ev := makeEvidence(repo, "main", "ref1", "//pkg:test", src, model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence/distinct?field=source&q="+url.QueryEscape("alice"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := decodeJSON[distinctResponse](t, resp)

	assert.ElementsMatch(t, []string{"alice_" + tag, "Alice2_" + tag}, got.Values)
}

func TestDistinctRejectsUnknownField(t *testing.T) {
	resp := getJSON(t, "/api/v1/evidence/distinct?field=metadata")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestDistinctRequiresField(t *testing.T) {
	resp := getJSON(t, "/api/v1/evidence/distinct")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestDistinctRespectsLimit(t *testing.T) {
	tag := uuid.New().String()[:8]
	for i := 0; i < 5; i++ {
		repo := "org/limit_" + tag + "_" + string(rune('a'+i))
		ev := makeEvidence(repo, "main", "ref1", "//pkg:test", "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence/distinct?field=repo&q="+url.QueryEscape(tag)+"&limit=3")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	got := decodeJSON[distinctResponse](t, resp)

	assert.Len(t, got.Values, 3)
}
