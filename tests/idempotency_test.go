package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/model"
)

// A client that sends the same submission twice must end up with one record.
//
// The case this is for is not a post that failed — that one is easy, and the
// client simply sends it again. It is the post that succeeded while the
// response was lost, after which the client cannot tell whether the store has
// the record and has to choose between losing evidence and duplicating it.
// client_record_id is what lets it send again and find out.

// withClientRecordID marshals a record and puts a client_record_id beside it,
// which is how a client sends the field over the wire.
func withClientRecordID(t *testing.T, e model.EvidenceCreate, id string) map[string]any {
	t.Helper()
	b, err := json.Marshal(e)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	m["client_record_id"] = id
	return m
}

func TestSameClientRecordIDFilesOneRecord(t *testing.T) {
	id := uuid.NewString()
	ev := makeEvidence("org/idem", "main", "aaa111", "//pkg:resend", "ci-bot", model.ResultPass)
	body := withClientRecordID(t, ev, id)

	first := postJSON(t, "/api/v1/evidence", body)
	require.Equal(t, http.StatusCreated, first.StatusCode)
	created := decodeJSON[model.Evidence](t, first)
	require.NotEqual(t, uuid.Nil, created.ID)

	// The same submission again. Not a second record, and not an error either:
	// the client is asking what happened to the one it already sent.
	second := postJSON(t, "/api/v1/evidence", body)
	assert.Equal(t, http.StatusOK, second.StatusCode,
		"a repeat submission is not a creation, so it is not a 201")
	again := decodeJSON[model.Evidence](t, second)

	assert.Equal(t, created.ID, again.ID, "the client must learn which record its submission became")
	assert.Equal(t, created.IngestedAt, again.IngestedAt, "the stored record is not touched by a repeat")

	// And the store really does hold one, not two.
	assert.Equal(t, 1, countEvidence(t, "org/idem", "//pkg:resend"))
}

// The response carries the field back, so a client reconciling a queue can
// match what it sent against what the store has.
func TestClientRecordIDComesBack(t *testing.T) {
	id := uuid.NewString()
	ev := makeEvidence("org/idem", "main", "bbb222", "//pkg:echo", "ci-bot", model.ResultPass)

	resp := postJSON(t, "/api/v1/evidence", withClientRecordID(t, ev, id))
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := decodeJSON[model.Evidence](t, resp)

	require.NotNil(t, created.ClientRecordID)
	assert.Equal(t, id, created.ClientRecordID.String())

	fetched := decodeJSON[model.Evidence](t, getJSON(t, "/api/v1/evidence/"+created.ID.String()))
	require.NotNil(t, fetched.ClientRecordID)
	assert.Equal(t, id, fetched.ClientRecordID.String())
}

// Nothing changes for a client that does not send the field, which is every
// client that exists today.
func TestWithoutClientRecordIDNothingIsDeduplicated(t *testing.T) {
	ev := makeEvidence("org/idem", "main", "ccc333", "//pkg:twice", "ci-bot", model.ResultPass)

	for range 2 {
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		got := decodeJSON[model.Evidence](t, resp)
		assert.Nil(t, got.ClientRecordID)
	}

	// Two genuine runs of the same procedure with the same verdict are two
	// records. Without a token from the client, the store has no business
	// deciding they are one.
	assert.Equal(t, 2, countEvidence(t, "org/idem", "//pkg:twice"))
}

func TestMalformedClientRecordIDIsRejected(t *testing.T) {
	ev := makeEvidence("org/idem", "main", "ddd444", "//pkg:malformed", "ci-bot", model.ResultPass)

	for _, bad := range []string{"not-a-uuid", "", "12345"} {
		resp := postJSON(t, "/api/v1/evidence", withClientRecordID(t, ev, bad))
		require.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode, "value %q", bad)

		body := decodeJSON[map[string]any](t, resp)
		assert.Contains(t, fmt.Sprintf("%v", body), "client_record_id",
			"the error should name the field that was wrong")
	}

	assert.Equal(t, 0, countEvidence(t, "org/idem", "//pkg:malformed"))
}

// --- Batches ---

// The batch case is the one that matters for a sync: lose one response for
// twelve records and all twelve are in doubt at once.
func TestRepeatedBatchFilesEachRecordOnce(t *testing.T) {
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString()}
	records := make([]map[string]any, len(ids))
	for i, id := range ids {
		ev := makeEvidence("org/idem-batch", "main", "eee555",
			fmt.Sprintf("//pkg:batch_%d", i), "ci-bot", model.ResultPass)
		records[i] = withClientRecordID(t, ev, id)
	}

	first := postJSON(t, "/api/v1/evidence/batch", map[string]any{"records": records})
	require.Equal(t, http.StatusCreated, first.StatusCode)
	firstResults := decodeJSON[model.BatchResponse](t, first).Results
	require.Len(t, firstResults, len(ids))
	for _, r := range firstResults {
		assert.Equal(t, "created", r.Status)
	}

	second := postJSON(t, "/api/v1/evidence/batch", map[string]any{"records": records})
	require.Equal(t, http.StatusCreated, second.StatusCode,
		"a batch of records the store already has is not a failed batch")
	secondResults := decodeJSON[model.BatchResponse](t, second).Results
	require.Len(t, secondResults, len(ids))

	for i, r := range secondResults {
		assert.Equal(t, "duplicate", r.Status, "record %d", i)
		assert.Equal(t, firstResults[i].ID, r.ID,
			"a duplicate names the record it turned into, so the client can drop it")
	}

	for i := range ids {
		assert.Equal(t, 1, countEvidence(t, "org/idem-batch", fmt.Sprintf("//pkg:batch_%d", i)))
	}
}

// A queue that managed to send some of its records before the link dropped
// comes back with a mixture, and each record is answered on its own terms.
func TestBatchMixesCreatedAndDuplicate(t *testing.T) {
	sent := uuid.NewString()
	ev := makeEvidence("org/idem-mixed", "main", "fff666", "//pkg:already", "ci-bot", model.ResultPass)
	require.Equal(t, http.StatusCreated,
		postJSON(t, "/api/v1/evidence", withClientRecordID(t, ev, sent)).StatusCode)

	fresh := makeEvidence("org/idem-mixed", "main", "fff666", "//pkg:new", "ci-bot", model.ResultFail)
	resp := postJSON(t, "/api/v1/evidence/batch", map[string]any{"records": []map[string]any{
		withClientRecordID(t, ev, sent),
		withClientRecordID(t, fresh, uuid.NewString()),
	}})
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	results := decodeJSON[model.BatchResponse](t, resp).Results
	require.Len(t, results, 2)
	assert.Equal(t, "duplicate", results[0].Status)
	assert.Equal(t, "created", results[1].Status)
}

// The same token twice inside one batch. A client should not send this, but a
// queue that was edited on a phone with a flaky screen can, and filing it
// twice would be the same wrong answer as filing it across two calls.
func TestRepeatWithinOneBatchIsOneRecord(t *testing.T) {
	id := uuid.NewString()
	ev := makeEvidence("org/idem-self", "main", "ggg777", "//pkg:self", "ci-bot", model.ResultPass)
	body := withClientRecordID(t, ev, id)

	resp := postJSON(t, "/api/v1/evidence/batch", map[string]any{"records": []map[string]any{body, body}})
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	results := decodeJSON[model.BatchResponse](t, resp).Results
	require.Len(t, results, 2)
	assert.Equal(t, "created", results[0].Status)
	assert.Equal(t, "duplicate", results[1].Status)
	assert.Equal(t, results[0].ID, results[1].ID)

	assert.Equal(t, 1, countEvidence(t, "org/idem-self", "//pkg:self"))
}

// A malformed token is reported against its own record and does not stop the
// rest of the batch, which is how this handler already treats bad data.
func TestMalformedClientRecordIDInBatchIsPerRecord(t *testing.T) {
	good := makeEvidence("org/idem-bad", "main", "hhh888", "//pkg:good", "ci-bot", model.ResultPass)
	bad := makeEvidence("org/idem-bad", "main", "hhh888", "//pkg:bad", "ci-bot", model.ResultPass)

	resp := postJSON(t, "/api/v1/evidence/batch", map[string]any{"records": []map[string]any{
		withClientRecordID(t, good, uuid.NewString()),
		withClientRecordID(t, bad, "not-a-uuid"),
	}})
	require.Equal(t, http.StatusMultiStatus, resp.StatusCode)

	results := decodeJSON[model.BatchResponse](t, resp).Results
	require.Len(t, results, 2)
	assert.Equal(t, "created", results[0].Status)
	assert.Equal(t, "error", results[1].Status)
	assert.Contains(t, results[1].Error, "client_record_id")

	assert.Equal(t, 1, countEvidence(t, "org/idem-bad", "//pkg:good"))
	assert.Equal(t, 0, countEvidence(t, "org/idem-bad", "//pkg:bad"))
}

// countEvidence counts stored records matching a repo and procedure, which is
// what "did that file one record or two" comes down to.
func countEvidence(t *testing.T, repo, procedureRef string) int {
	t.Helper()
	var n int
	err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM evidence WHERE repo = $1 AND procedure_ref = $2`,
		repo, procedureRef).Scan(&n)
	require.NoError(t, err)
	return n
}
