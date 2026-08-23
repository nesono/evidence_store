package tests

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/model"
)

// ---------------------------------------------------------------------------
// Tests: source is bound to the caller's identity
//
// A record's source is what a reader goes on months later to ask who ran this
// and whether to believe them. Until phase 3 it was free text the client set to
// whatever it liked.
// ---------------------------------------------------------------------------

// createdSource posts one record and reports what the store filed it under.
func createdSource(t *testing.T, ts, key string, ev model.EvidenceCreate) (int, string) {
	t.Helper()
	resp := doRequest(t, http.MethodPost, ts+"/api/v1/evidence", "Bearer "+key, ev)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return resp.StatusCode, ""
	}
	var created model.Evidence
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
	return resp.StatusCode, created.Source
}

// A build robot's useful attribution is the build URL, not the robot. That is
// the whole reason ci is a role and not a flag.
func TestCIKeyMayWriteAnySource(t *testing.T) {
	ts := setupRBACServer(t, nil)
	key := issueKey(t, rbacSubject(t, "robot"), "ci")

	status, source := createdSource(t, ts.URL, key,
		makeEvidence("org/src_ci", "main", "ref1", "//pkg:test", "https://ci.example.com/build/1234", model.ResultPass))
	assert.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "https://ci.example.com/build/1234", source)
}

func TestContributorIsPinnedToItsOwnSubject(t *testing.T) {
	ts := setupRBACServer(t, nil)
	subject := rbacSubject(t, "human")
	key := issueKey(t, subject, "contributor")

	t.Run("its own name is accepted", func(t *testing.T) {
		status, source := createdSource(t, ts.URL, key,
			makeEvidence("org/src_pin", "main", "ref1", "//pkg:test", subject, model.ResultPass))
		assert.Equal(t, http.StatusCreated, status)
		assert.Equal(t, subject, source)
	})

	// Saying nothing is not a claim about anybody, so the server makes the true
	// one. This is what makes source trustworthy enough to attribute a manual
	// test result to a person.
	t.Run("an empty source is filled in", func(t *testing.T) {
		status, source := createdSource(t, ts.URL, key,
			makeEvidence("org/src_pin", "main", "ref2", "//pkg:test", "", model.ResultPass))
		assert.Equal(t, http.StatusCreated, status)
		assert.Equal(t, subject, source)
	})

	t.Run("somebody else's name is refused", func(t *testing.T) {
		resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "Bearer "+key,
			makeEvidence("org/src_pin", "main", "ref3", "//pkg:test", "ci:nightly", model.ResultPass))
		defer resp.Body.Close()
		assert.Equal(t, http.StatusForbidden, resp.StatusCode)

		var body map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		assert.Contains(t, body["error"], subject, "the caller should be told what the server expected")
	})
}

// admin deliberately does not subsume ci: writing history in another party's
// name is always a deliberate grant, even for an administrator.
func TestAdminWithoutCIIsPinnedToo(t *testing.T) {
	ts := setupRBACServer(t, nil)
	subject := rbacSubject(t, "boss")
	key := issueKey(t, subject, "admin")

	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "Bearer "+key,
		makeEvidence("org/src_admin", "main", "ref1", "//pkg:test", "somebody-else", model.ResultPass))
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// Holding both roles is how an administrator backfills evidence in
	// somebody else's name.
	bothKey := issueKey(t, rbacSubject(t, "boss-and-robot"), "admin", "ci")
	status, source := createdSource(t, ts.URL, bothKey,
		makeEvidence("org/src_admin", "main", "ref2", "//pkg:test", "somebody-else", model.ResultPass))
	assert.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "somebody-else", source)
}

// ---------------------------------------------------------------------------
// Tests: batches
// ---------------------------------------------------------------------------

func postBatch(t *testing.T, ts, key string, records ...model.EvidenceCreate) *http.Response {
	t.Helper()
	return doRequest(t, http.MethodPost, ts+"/api/v1/evidence/batch", "Bearer "+key,
		model.BatchRequest{Records: records})
}

// The batch handler reports a malformed record and carries on with the others.
// A source the caller may not write is not malformed data — it is a claim about
// who they are — so it takes the whole batch down rather than leaving evidence
// filed under a name whose owner never filed it.
func TestOneUnownedSourceRejectsTheWholeBatch(t *testing.T) {
	ts := setupRBACServer(t, nil)
	subject := rbacSubject(t, "batcher")
	key := issueKey(t, subject, "contributor")

	resp := postBatch(t, ts.URL, key,
		makeEvidence("org/src_batch", "main", "ok1", "//pkg:test", subject, model.ResultPass),
		makeEvidence("org/src_batch", "main", "bad", "//pkg:test", "ci:nightly", model.ResultPass),
		makeEvidence("org/src_batch", "main", "ok2", "//pkg:test", "", model.ResultPass),
	)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(t, body["error"], "record 1", "the caller should be told which record")

	// Nothing was filed, including the two rows that were the caller's to write.
	resp = doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence?repo=org/src_batch", "Bearer "+key, nil)
	defer resp.Body.Close()
	var listed struct {
		Records []model.Evidence `json:"records"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&listed))
	assert.Empty(t, listed.Records, "a refused batch must not half-commit")
}

func TestBatchFillsInEmptySourcesForItsCaller(t *testing.T) {
	ts := setupRBACServer(t, nil)
	subject := rbacSubject(t, "batchfill")
	key := issueKey(t, subject, "contributor")

	resp := postBatch(t, ts.URL, key,
		makeEvidence("org/src_batchfill", "main", "r1", "//pkg:test", "", model.ResultPass),
		makeEvidence("org/src_batchfill", "main", "r2", "//pkg:test", subject, model.ResultPass),
	)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	listResp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence?repo=org/src_batchfill", "Bearer "+key, nil)
	defer listResp.Body.Close()
	var listed struct {
		Records []model.Evidence `json:"records"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&listed))
	require.Len(t, listed.Records, 2)
	for _, rec := range listed.Records {
		assert.Equal(t, subject, rec.Source)
	}
}

// A ci key posting a batch of build results is the common case, and it must not
// have become slower or stricter.
func TestBatchFromCIKeepsEverySource(t *testing.T) {
	ts := setupRBACServer(t, nil)
	key := issueKey(t, rbacSubject(t, "batchci"), "ci")

	resp := postBatch(t, ts.URL, key,
		makeEvidence("org/src_batchci", "main", "r1", "//pkg:a", "https://ci/build/1", model.ResultPass),
		makeEvidence("org/src_batchci", "main", "r2", "//pkg:b", "https://ci/build/2", model.ResultFail),
	)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	listResp := doRequest(t, http.MethodGet, ts.URL+"/api/v1/evidence?repo=org/src_batchci", "Bearer "+key, nil)
	defer listResp.Body.Close()
	var listed struct {
		Records []model.Evidence `json:"records"`
	}
	require.NoError(t, json.NewDecoder(listResp.Body).Decode(&listed))
	require.Len(t, listed.Records, 2)
	sources := []string{listed.Records[0].Source, listed.Records[1].Source}
	assert.ElementsMatch(t, []string{"https://ci/build/1", "https://ci/build/2"}, sources)
}

// A malformed record still gets the old treatment: reported, with the rest of
// the batch filed. Binding source must not have turned data errors into
// all-or-nothing.
func TestBatchStillReportsBadRecordsIndividually(t *testing.T) {
	ts := setupRBACServer(t, nil)
	key := issueKey(t, rbacSubject(t, "batchmixed"), "ci")

	bad := makeEvidence("org/src_batchmixed", "main", "r2", "//pkg:b", "https://ci/build/2", model.ResultPass)
	bad.EvidenceType = "not-a-type"

	resp := postBatch(t, ts.URL, key,
		makeEvidence("org/src_batchmixed", "main", "r1", "//pkg:a", "https://ci/build/1", model.ResultPass),
		bad,
	)
	defer resp.Body.Close()
	require.Equal(t, http.StatusMultiStatus, resp.StatusCode)

	var body model.BatchResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Len(t, body.Results, 2)
	assert.Equal(t, "created", body.Results[0].Status)
	assert.Equal(t, "error", body.Results[1].Status)
}

// ---------------------------------------------------------------------------
// Tests: the postures that must not change
// ---------------------------------------------------------------------------

// With nothing configured there is no identity to pin a source to, and the
// field means what it always meant. This is the default local-development
// posture and the one every existing test writes under.
func TestOpenStoreLeavesSourceAlone(t *testing.T) {
	ts := setupAuthServer(t, nil)
	defer ts.Close()

	status, source := createdSource(t, ts.URL, "",
		makeEvidence("org/src_open", "main", "ref1", "//pkg:test", "anything-at-all", model.ResultPass))
	assert.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "anything-at-all", source)

	// And an empty source is still the validation error it always was: there is
	// no subject to fill it in from.
	resp := doRequest(t, http.MethodPost, ts.URL+"/api/v1/evidence", "",
		makeEvidence("org/src_open", "main", "ref2", "//pkg:test", "", model.ResultPass))
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp.Body.Close()
}

// Posture 2 is untouched: an env rw key maps to ci, so the pipelines using one
// keep writing whatever source they wrote yesterday.
func TestConfiguredRWKeyKeepsWritingAnySource(t *testing.T) {
	ts := setupAuthServer(t, []config.APIKey{{Key: "rw-source-key"}})
	defer ts.Close()

	status, source := createdSource(t, ts.URL, "rw-source-key",
		makeEvidence("org/src_envkey", "main", "ref1", "//pkg:test", "https://ci/build/7", model.ResultPass))
	assert.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "https://ci/build/7", source)
}
