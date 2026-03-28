package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/migrate"
	"github.com/nesono/evidence-store/internal/model"
	"github.com/nesono/evidence-store/internal/server"
)

var testServer *httptest.Server

func TestMain(m *testing.M) {
	ctx := context.Background()

	// Start a Postgres container.
	pgContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_DB":       "evidence_test",
				"POSTGRES_USER":     "evidence",
				"POSTGRES_PASSWORD": "evidence",
			},
			WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start postgres container: %v\n", err)
		os.Exit(1)
	}
	defer pgContainer.Terminate(ctx)

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432")

	dbURL := fmt.Sprintf("postgres://evidence:evidence@%s:%s/evidence_test?sslmode=disable", host, port.Port())

	// Find migrations directory (relative to this test file).
	migrationsPath, _ := filepath.Abs(filepath.Join("..", "migrations"))
	if err := migrate.Run(dbURL, migrationsPath); err != nil {
		fmt.Fprintf(os.Stderr, "failed to run migrations: %v\n", err)
		os.Exit(1)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create pool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	cfg := &config.Config{
		DatabaseURL:     dbURL,
		ListenAddr:      ":0",
		DefaultPageSize: 100,
		MaxPageSize:     1000,
		MaxBatchSize:    1000,
		LogLevel:        "ERROR",
	}

	srv := server.New(cfg, pool)
	testServer = httptest.NewServer(srv.Handler())
	defer testServer.Close()

	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func postJSON(t *testing.T, path string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	resp, err := http.Post(testServer.URL+path, "application/json", bytes.NewReader(b))
	require.NoError(t, err)
	return resp
}

func getJSON(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	require.NoError(t, err)
	return resp
}

func decodeJSON[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	defer resp.Body.Close()
	var v T
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&v))
	return v
}

func makeEvidence(repo, branch, rcsRef, procedureRef, source string, result model.EvidenceResult) model.EvidenceCreate {
	return model.EvidenceCreate{
		Repo:         repo,
		Branch:       branch,
		RCSRef:       rcsRef,
		ProcedureRef: procedureRef,
		EvidenceType: "bazel",
		Source:       source,
		Result:       result,
		FinishedAt:   time.Now().UTC().Truncate(time.Microsecond),
	}
}

// ---------------------------------------------------------------------------
// Tests: Single Ingest
// ---------------------------------------------------------------------------

func TestCreateEvidence(t *testing.T) {
	ev := makeEvidence("org/repo1", "main", "aaa111", "//pkg:test_create", "ci-bot", model.ResultPass)
	ev.Metadata = json.RawMessage(`{"duration_s": 1.5, "tags": ["unit"]}`)

	resp := postJSON(t, "/api/v1/evidence", ev)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	result := decodeJSON[model.Evidence](t, resp)
	assert.NotEqual(t, uuid.Nil, result.ID)
	assert.Equal(t, "org/repo1", result.Repo)
	assert.Equal(t, "main", result.Branch)
	assert.Equal(t, "aaa111", result.RCSRef)
	assert.Equal(t, "//pkg:test_create", result.ProcedureRef)
	assert.Equal(t, "bazel", result.EvidenceType)
	assert.Equal(t, model.ResultPass, result.Result)
	assert.False(t, result.IngestedAt.IsZero())
}

func TestCreateEvidenceMissingFields(t *testing.T) {
	// Missing repo, branch, rcs_ref, etc.
	resp := postJSON(t, "/api/v1/evidence", map[string]string{"result": "PASS"})
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp.Body.Close()
}

func TestCreateEvidenceInvalidResult(t *testing.T) {
	ev := makeEvidence("org/repo1", "main", "aaa111", "//pkg:test", "ci", model.EvidenceResult("INVALID"))
	resp := postJSON(t, "/api/v1/evidence", ev)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp.Body.Close()
}

func TestCreateEvidenceInvalidEvidenceType(t *testing.T) {
	ev := makeEvidence("org/repo1", "main", "aaa111", "//pkg:test", "ci", model.ResultPass)
	ev.EvidenceType = "INVALID-TYPE!"
	resp := postJSON(t, "/api/v1/evidence", ev)
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: Get by ID
// ---------------------------------------------------------------------------

func TestGetEvidenceByID(t *testing.T) {
	ev := makeEvidence("org/repo_get", "main", "bbb222", "//pkg:test_get", "ci", model.ResultFail)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	created := decodeJSON[model.Evidence](t, resp)

	resp = getJSON(t, "/api/v1/evidence/"+created.ID.String())
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	fetched := decodeJSON[model.Evidence](t, resp)
	assert.Equal(t, created.ID, fetched.ID)
	assert.Equal(t, model.ResultFail, fetched.Result)
}

func TestGetEvidenceNotFound(t *testing.T) {
	resp := getJSON(t, "/api/v1/evidence/"+uuid.New().String())
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

func TestGetEvidenceInvalidID(t *testing.T) {
	resp := getJSON(t, "/api/v1/evidence/not-a-uuid")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: Query / List
// ---------------------------------------------------------------------------

func TestListEvidenceFilterByRepo(t *testing.T) {
	repo := "org/filter_repo_" + uuid.New().String()[:8]
	ev := makeEvidence(repo, "main", "ccc333", "//pkg:test_list", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = getJSON(t, "/api/v1/evidence?repo="+repo)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)
	assert.Equal(t, repo, result.Records[0].Repo)
}

func TestListEvidenceFilterByResult(t *testing.T) {
	repo := "org/filter_result_" + uuid.New().String()[:8]

	for _, r := range []model.EvidenceResult{model.ResultPass, model.ResultFail, model.ResultPass} {
		ev := makeEvidence(repo, "main", "ddd444", "//pkg:test", "ci", r)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&result=FAIL")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)
	assert.Equal(t, model.ResultFail, result.Records[0].Result)
}

func TestListEvidenceFilterByMultipleResults(t *testing.T) {
	repo := "org/filter_multi_" + uuid.New().String()[:8]

	for _, r := range []model.EvidenceResult{model.ResultPass, model.ResultFail, model.ResultError} {
		ev := makeEvidence(repo, "main", "eee555", "//pkg:test", "ci", r)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&result=PASS,ERROR")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2)
}

func TestListEvidenceFilterByProcedureRefPrefix(t *testing.T) {
	repo := "org/filter_prefix_" + uuid.New().String()[:8]

	ev1 := makeEvidence(repo, "main", "fff666", "//pkg/a:test1", "ci", model.ResultPass)
	ev2 := makeEvidence(repo, "main", "fff666", "//pkg/a:test2", "ci", model.ResultPass)
	ev3 := makeEvidence(repo, "main", "fff666", "//pkg/b:test1", "ci", model.ResultPass)

	for _, ev := range []model.EvidenceCreate{ev1, ev2, ev3} {
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&procedure_ref=//pkg/a:*")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 2)
}

func TestListEvidenceFilterByTimeRange(t *testing.T) {
	repo := "org/filter_time_" + uuid.New().String()[:8]

	now := time.Now().UTC()
	ev1 := makeEvidence(repo, "main", "ggg777", "//pkg:test", "ci", model.ResultPass)
	ev1.FinishedAt = now.Add(-2 * time.Hour)
	ev2 := makeEvidence(repo, "main", "ggg777", "//pkg:test2", "ci", model.ResultPass)
	ev2.FinishedAt = now.Add(-30 * time.Minute)

	for _, ev := range []model.EvidenceCreate{ev1, ev2} {
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	after := now.Add(-1 * time.Hour).Format(time.RFC3339)
	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&finished_after="+after)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)
}

func TestListEvidenceFilterByTags(t *testing.T) {
	repo := "org/filter_tags_" + uuid.New().String()[:8]

	ev1 := makeEvidence(repo, "main", "hhh888", "//pkg:test1", "ci", model.ResultPass)
	ev1.Metadata = json.RawMessage(`{"tags": ["nightly", "x86_64"]}`)
	ev2 := makeEvidence(repo, "main", "hhh888", "//pkg:test2", "ci", model.ResultPass)
	ev2.Metadata = json.RawMessage(`{"tags": ["nightly", "arm64"]}`)

	for _, ev := range []model.EvidenceCreate{ev1, ev2} {
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	resp := getJSON(t, "/api/v1/evidence?repo="+repo+"&tags=nightly,x86_64")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Len(t, result.Records, 1)
}

func TestListEvidenceEmptyResult(t *testing.T) {
	resp := getJSON(t, "/api/v1/evidence?repo=nonexistent/repo")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Empty(t, result.Records)
}

// ---------------------------------------------------------------------------
// Tests: Cursor Pagination
// ---------------------------------------------------------------------------

func TestListEvidencePagination(t *testing.T) {
	repo := "org/pagination_" + uuid.New().String()[:8]

	// Insert 5 records.
	for i := 0; i < 5; i++ {
		ev := makeEvidence(repo, "main", "ppp999", fmt.Sprintf("//pkg:test_%d", i), "ci", model.ResultPass)
		resp := postJSON(t, "/api/v1/evidence", ev)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()
	}

	// Fetch page 1 (limit 2).
	resp := getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2", repo))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	page1 := decodeJSON[listResponse](t, resp)
	assert.Len(t, page1.Records, 2)
	require.NotNil(t, page1.NextCursor, "expected a next_cursor for page 1")

	// Fetch page 2.
	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2&cursor=%s", repo, *page1.NextCursor))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	page2 := decodeJSON[listResponse](t, resp)
	assert.Len(t, page2.Records, 2)
	require.NotNil(t, page2.NextCursor, "expected a next_cursor for page 2")

	// Fetch page 3 (last page, should have 1 record).
	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&limit=2&cursor=%s", repo, *page2.NextCursor))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	page3 := decodeJSON[listResponse](t, resp)
	assert.Len(t, page3.Records, 1)
	assert.Nil(t, page3.NextCursor, "expected no next_cursor on last page")

	// Verify no overlap between pages.
	allIDs := make(map[uuid.UUID]bool)
	for _, r := range append(append(page1.Records, page2.Records...), page3.Records...) {
		assert.False(t, allIDs[r.ID], "duplicate ID across pages")
		allIDs[r.ID] = true
	}
	assert.Len(t, allIDs, 5)
}

// ---------------------------------------------------------------------------
// Tests: Batch Ingest
// ---------------------------------------------------------------------------

func TestBatchCreateAllValid(t *testing.T) {
	repo := "org/batch_valid_" + uuid.New().String()[:8]
	records := []model.EvidenceCreate{
		makeEvidence(repo, "main", "batch1", "//pkg:test_a", "ci", model.ResultPass),
		makeEvidence(repo, "main", "batch1", "//pkg:test_b", "ci", model.ResultFail),
	}

	resp := postJSON(t, "/api/v1/evidence/batch", model.BatchRequest{Records: records})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	result := decodeJSON[model.BatchResponse](t, resp)
	assert.Len(t, result.Results, 2)
	for _, r := range result.Results {
		assert.Equal(t, "created", r.Status)
		assert.NotEqual(t, uuid.Nil, r.ID)
	}
}

func TestBatchCreatePartialFailure(t *testing.T) {
	repo := "org/batch_partial_" + uuid.New().String()[:8]
	records := []model.EvidenceCreate{
		makeEvidence(repo, "main", "batch2", "//pkg:test_a", "ci", model.ResultPass),
		{}, // Invalid — missing all fields.
		makeEvidence(repo, "main", "batch2", "//pkg:test_c", "ci", model.ResultFail),
	}

	resp := postJSON(t, "/api/v1/evidence/batch", model.BatchRequest{Records: records})
	assert.Equal(t, http.StatusMultiStatus, resp.StatusCode)

	result := decodeJSON[model.BatchResponse](t, resp)
	assert.Len(t, result.Results, 3)
	assert.Equal(t, "created", result.Results[0].Status)
	assert.Equal(t, "error", result.Results[1].Status)
	assert.NotEmpty(t, result.Results[1].Error)
	assert.Equal(t, "created", result.Results[2].Status)
}

func TestBatchCreateEmpty(t *testing.T) {
	resp := postJSON(t, "/api/v1/evidence/batch", model.BatchRequest{Records: []model.EvidenceCreate{}})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

func TestBatchCreateExceedsMax(t *testing.T) {
	records := make([]model.EvidenceCreate, 1001)
	resp := postJSON(t, "/api/v1/evidence/batch", model.BatchRequest{Records: records})
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Tests: Inheritance
// ---------------------------------------------------------------------------

func TestCreateInheritance(t *testing.T) {
	resp := postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo:          "org/inh_repo",
		SourceRCSRef:  "src111",
		TargetRCSRef:  "tgt222",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "no changes in pkg/",
		CreatedBy:     "ci-bot",
	})
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	result := decodeJSON[model.InheritanceDeclaration](t, resp)
	assert.NotEqual(t, uuid.Nil, result.ID)
	assert.Equal(t, "org/inh_repo", result.Repo)
	assert.Equal(t, "src111", result.SourceRCSRef)
	assert.Equal(t, "tgt222", result.TargetRCSRef)
}

func TestCreateInheritanceMissingFields(t *testing.T) {
	resp := postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo: "org/inh_repo",
	})
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
	resp.Body.Close()
}

func TestListInheritance(t *testing.T) {
	repo := "org/inh_list_" + uuid.New().String()[:8]

	resp := postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo:          repo,
		SourceRCSRef:  "src_a",
		TargetRCSRef:  "tgt_b",
		Scope:         json.RawMessage(`[]`),
		Justification: "test",
		CreatedBy:     "user",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	resp = getJSON(t, "/api/v1/inheritance?repo="+repo+"&target_rcs_ref=tgt_b")
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[inheritanceListResponse](t, resp)
	assert.Len(t, result.Declarations, 1)
	assert.Equal(t, repo, result.Declarations[0].Repo)
}

// ---------------------------------------------------------------------------
// Tests: Inheritance Resolution in Evidence Query
// ---------------------------------------------------------------------------

func TestEvidenceQueryWithInheritance(t *testing.T) {
	repo := "org/inh_resolve_" + uuid.New().String()[:8]

	// Insert evidence for source commit.
	ev := makeEvidence(repo, "main", "src_commit", "//pkg:test_inh", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Create inheritance: target_commit inherits from src_commit.
	resp = postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo:          repo,
		SourceRCSRef:  "src_commit",
		TargetRCSRef:  "target_commit",
		Scope:         json.RawMessage(`["//pkg:*"]`),
		Justification: "no changes",
		CreatedBy:     "ci",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Query for target_commit — should include inherited evidence.
	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&rcs_ref=target_commit", repo))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	require.Len(t, result.Records, 1)
	assert.True(t, *result.Records[0].Inherited)
	assert.NotNil(t, result.Records[0].InheritanceDeclaration)
}

func TestEvidenceQueryWithoutInheritance(t *testing.T) {
	repo := "org/inh_skip_" + uuid.New().String()[:8]

	// Insert evidence for source commit.
	ev := makeEvidence(repo, "main", "src_c2", "//pkg:test", "ci", model.ResultPass)
	resp := postJSON(t, "/api/v1/evidence", ev)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Create inheritance.
	resp = postJSON(t, "/api/v1/inheritance", model.InheritanceCreate{
		Repo:          repo,
		SourceRCSRef:  "src_c2",
		TargetRCSRef:  "tgt_c2",
		Scope:         json.RawMessage(`[]`),
		Justification: "test",
		CreatedBy:     "ci",
	})
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Query with include_inherited=false — should return nothing.
	resp = getJSON(t, fmt.Sprintf("/api/v1/evidence?repo=%s&rcs_ref=tgt_c2&include_inherited=false", repo))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	result := decodeJSON[listResponse](t, resp)
	assert.Empty(t, result.Records)
}

// ---------------------------------------------------------------------------
// Tests: Health Check
// ---------------------------------------------------------------------------

func TestHealthCheck(t *testing.T) {
	resp := getJSON(t, "/healthz")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}

// ---------------------------------------------------------------------------
// Response types for decoding
// ---------------------------------------------------------------------------

type listResponse struct {
	Records    []model.EvidenceResponse `json:"records"`
	NextCursor *string                  `json:"next_cursor"`
}

type inheritanceListResponse struct {
	Declarations []model.InheritanceDeclaration `json:"declarations"`
}
