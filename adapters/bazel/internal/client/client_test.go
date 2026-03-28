package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPostBatch(t *testing.T) {
	var received batchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/evidence/batch", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		json.NewDecoder(r.Body).Decode(&received)

		resp := BatchResponse{}
		for i := range received.Records {
			resp.Results = append(resp.Results, BatchRecordStatus{
				Index:  i,
				ID:     "fake-uuid",
				Status: "created",
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := New(server.URL)

	records := []EvidenceRecord{
		{
			Repo:         "org/repo",
			Branch:       "main",
			RCSRef:       "abc123",
			ProcedureRef: "//pkg:test",
			EvidenceType: "bazel",
			Source:       "ci",
			Result:       "PASS",
			FinishedAt:   "2026-03-28T10:00:00Z",
		},
	}

	responses, err := c.PostBatch(context.Background(), records)
	require.NoError(t, err)
	require.Len(t, responses, 1)
	assert.Len(t, responses[0].Results, 1)
	assert.Equal(t, "created", responses[0].Results[0].Status)
}

func TestPostBatchWithAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer my-secret-key", r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(BatchResponse{})
	}))
	defer server.Close()

	c := New(server.URL, WithAPIKey("my-secret-key"))
	_, err := c.PostBatch(context.Background(), []EvidenceRecord{})
	require.NoError(t, err)
}

func TestPostBatchChunking(t *testing.T) {
	batchCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batchCount++
		var req batchRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := BatchResponse{}
		for i := range req.Records {
			resp.Results = append(resp.Results, BatchRecordStatus{Index: i, Status: "created"})
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := New(server.URL)

	// Create 2500 records — should result in 3 batches (1000+1000+500).
	records := make([]EvidenceRecord, 2500)
	for i := range records {
		records[i] = EvidenceRecord{
			Repo: "org/repo", Branch: "main", RCSRef: "abc",
			ProcedureRef: "//pkg:test", EvidenceType: "bazel",
			Source: "ci", Result: "PASS", FinishedAt: "2026-03-28T10:00:00Z",
		}
	}

	responses, err := c.PostBatch(context.Background(), records)
	require.NoError(t, err)
	assert.Equal(t, 3, batchCount)
	assert.Len(t, responses, 3)
}
