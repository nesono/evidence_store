package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nesono/evidence-store/adapters/bazel/internal/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validRecordOptions() recordOptions {
	return recordOptions{
		Repo:         "nesono/evidence_store",
		Branch:       "main",
		RCSRef:       "abc123",
		ProcedureRef: "//pkg:failure_test",
		Result:       "PASS",
	}
}

func TestBuildRecord_Minimal(t *testing.T) {
	rec, err := buildRecord(validRecordOptions())
	require.NoError(t, err)

	assert.Equal(t, "nesono/evidence_store", rec.Repo)
	assert.Equal(t, "PASS", rec.Result)
	assert.Equal(t, "bazel_manual", rec.EvidenceType)
	assert.Equal(t, "//pkg:failure_test", rec.ProcedureRef)
	assert.Nil(t, rec.Metadata)

	_, err = time.Parse(time.RFC3339, rec.FinishedAt)
	assert.NoError(t, err, "FinishedAt should default to RFC3339 now")
}

func TestBuildRecord_NormalizesResultCase(t *testing.T) {
	opts := validRecordOptions()
	opts.Result = "  fail  "
	rec, err := buildRecord(opts)
	require.NoError(t, err)
	assert.Equal(t, "FAIL", rec.Result)
}

func TestBuildRecord_InvalidResult(t *testing.T) {
	opts := validRecordOptions()
	opts.Result = "MAYBE"
	_, err := buildRecord(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be one of PASS, FAIL, ERROR, SKIPPED")
}

func TestBuildRecord_RequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*recordOptions)
		wantMsg string
	}{
		{"missing procedure-ref", func(o *recordOptions) { o.ProcedureRef = "" }, "--procedure-ref is required"},
		{"missing repo", func(o *recordOptions) { o.Repo = "" }, "--repo is required"},
		{"missing rcs-ref", func(o *recordOptions) { o.RCSRef = "" }, "--rcs-ref is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := validRecordOptions()
			tc.mutate(&opts)
			_, err := buildRecord(opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestBuildRecord_MergesMetadata(t *testing.T) {
	opts := validRecordOptions()
	opts.Notes = "expected static_assert pattern found"
	opts.Tags = "failure_test, version_too_old ,"
	opts.DurationMS = 150
	opts.InvocationID = "inv-uuid"
	opts.Metadata = `{"weather":"sunny","trial":3}`

	rec, err := buildRecord(opts)
	require.NoError(t, err)

	md := rec.Metadata
	require.NotNil(t, md)
	assert.Equal(t, "expected static_assert pattern found", md["notes"])
	assert.Equal(t, []string{"failure_test", "version_too_old"}, md["tags"])
	assert.Equal(t, int64(150), md["duration_ms"])
	assert.Equal(t, "inv-uuid", md["invocation_id"])
	assert.Equal(t, "sunny", md["weather"])
	assert.EqualValues(t, 3, md["trial"])
}

func TestBuildRecord_MetadataFlagsOverrideRawMetadata(t *testing.T) {
	// If user supplies metadata JSON that includes "notes", the explicit
	// --notes flag should win.
	opts := validRecordOptions()
	opts.Metadata = `{"notes":"from-json","other":"keep"}`
	opts.Notes = "from-flag"

	rec, err := buildRecord(opts)
	require.NoError(t, err)

	md := rec.Metadata
	require.NotNil(t, md)
	assert.Equal(t, "from-flag", md["notes"])
	assert.Equal(t, "keep", md["other"])
}

func TestBuildRecord_InvalidMetadataJSON(t *testing.T) {
	opts := validRecordOptions()
	opts.Metadata = "not json"
	_, err := buildRecord(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a JSON object")
}

func TestBuildRecord_FinishedAtPassthrough(t *testing.T) {
	opts := validRecordOptions()
	opts.FinishedAt = "2026-04-18T10:00:00Z"
	rec, err := buildRecord(opts)
	require.NoError(t, err)
	assert.Equal(t, "2026-04-18T10:00:00Z", rec.FinishedAt)
}

func TestBuildRecord_CustomEvidenceType(t *testing.T) {
	opts := validRecordOptions()
	opts.EvidenceType = "bazel_failure_test"
	rec, err := buildRecord(opts)
	require.NoError(t, err)
	assert.Equal(t, "bazel_failure_test", rec.EvidenceType)
}

func TestBuildRecord_EmptyTagsDoesNotAddKey(t *testing.T) {
	opts := validRecordOptions()
	opts.Tags = " , , "
	rec, err := buildRecord(opts)
	require.NoError(t, err)
	assert.Nil(t, rec.Metadata, "metadata should remain nil when tag string yields no tags")
}

func TestBuildRecord_ZeroDurationNotStored(t *testing.T) {
	opts := validRecordOptions()
	opts.DurationMS = 0
	rec, err := buildRecord(opts)
	require.NoError(t, err)
	assert.Nil(t, rec.Metadata)
}

func TestFindWorkspaceDir_LocatesConfigFile(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, ".evidence")
	require.NoError(t, os.MkdirAll(configDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("api_url: http://x\n"), 0o644))

	// BUILD_WORKSPACE_DIRECTORY takes precedence.
	t.Setenv("BUILD_WORKSPACE_DIRECTORY", root)
	got := findWorkspaceDir()
	// Resolve symlinks for macOS /var → /private/var.
	gotAbs, _ := filepath.EvalSymlinks(got)
	rootAbs, _ := filepath.EvalSymlinks(root)
	assert.Equal(t, rootAbs, gotAbs)
}

func TestFindWorkspaceDir_WalksUpward(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".evidence"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".evidence", "config.yaml"), []byte(""), 0o644))

	nested := filepath.Join(root, "a", "b", "c")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	t.Setenv("BUILD_WORKSPACE_DIRECTORY", nested)

	got := findWorkspaceDir()
	gotAbs, _ := filepath.EvalSymlinks(got)
	rootAbs, _ := filepath.EvalSymlinks(root)
	assert.Equal(t, rootAbs, gotAbs)
}

func TestFindWorkspaceDir_ReturnsEmptyWhenNotFound(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BUILD_WORKSPACE_DIRECTORY", root)
	assert.Equal(t, "", findWorkspaceDir())
}

func TestWriteRecord_ProducesParseableJSON(t *testing.T) {
	rec := client.EvidenceRecord{
		Repo:         "nesono/evidence_store",
		Branch:       "main",
		RCSRef:       "abc123",
		ProcedureRef: "//pkg:ft",
		EvidenceType: "bazel-failure-test",
		Result:       "PASS",
		FinishedAt:   "2026-04-18T10:00:00Z",
		Metadata:     map[string]any{"notes": "ok"},
	}
	var buf bytes.Buffer
	require.NoError(t, writeRecord(&buf, rec))

	var back client.EvidenceRecord
	require.NoError(t, json.Unmarshal(buf.Bytes(), &back))
	assert.Equal(t, rec.ProcedureRef, back.ProcedureRef)
	assert.Equal(t, rec.Result, back.Result)
}
