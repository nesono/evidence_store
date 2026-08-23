package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nesono/evidence-store/internal/blob"
)

func logWith(refs ...blob.Ref) string {
	log := "## Run 2\n1. Powered on the rig\n"
	for _, r := range refs {
		log += "![shot](" + r.Path() + ")\n"
	}
	return log
}

func photoURIs(t *testing.T, metadata json.RawMessage) []string {
	t.Helper()
	var fields struct {
		PhotoURIs []string `json:"photo_uris"`
	}
	require.NoError(t, json.Unmarshal(metadata, &fields))
	return fields.PhotoURIs
}

func TestAnnotateListsLoggedImagesUnderPhotoURIs(t *testing.T) {
	first := blob.Ref{Digest: blob.DigestOf([]byte("a")), Ext: "png"}
	second := blob.Ref{Digest: blob.DigestOf([]byte("b")), Ext: "jpg"}

	metadata, _ := json.Marshal(map[string]any{"observations": logWith(first, second)})
	annotated, refs, err := annotateBlobRefs(metadata)
	require.NoError(t, err)

	assert.Equal(t, []blob.Ref{first, second}, refs)
	assert.Equal(t, []string{first.Path(), second.Path()}, photoURIs(t, annotated))
}

// The log is the source of truth, but it must survive the round trip untouched:
// annotating metadata is not a licence to reflow what the tester wrote.
func TestAnnotateLeavesTheLogAlone(t *testing.T) {
	ref := blob.Ref{Digest: blob.DigestOf([]byte("a")), Ext: "png"}
	log := logWith(ref)

	metadata, _ := json.Marshal(map[string]any{"observations": log, "notes": "pedal soft"})
	annotated, _, err := annotateBlobRefs(metadata)
	require.NoError(t, err)

	var fields struct {
		Observations string `json:"observations"`
		Notes        string `json:"notes"`
	}
	require.NoError(t, json.Unmarshal(annotated, &fields))
	assert.Equal(t, log, fields.Observations)
	assert.Equal(t, "pedal soft", fields.Notes)
}

func TestAnnotateAddsToClientSuppliedPhotoURIs(t *testing.T) {
	ref := blob.Ref{Digest: blob.DigestOf([]byte("a")), Ext: "png"}

	metadata, _ := json.Marshal(map[string]any{
		"observations": logWith(ref),
		"photo_uris":   []string{"https://photos.example.com/run-42.jpg"},
	})
	annotated, _, err := annotateBlobRefs(metadata)
	require.NoError(t, err)

	// A client that keeps its photos somewhere else does not lose them because
	// the log happens to reference a blob as well.
	assert.Equal(t, []string{"https://photos.example.com/run-42.jpg", ref.Path()}, photoURIs(t, annotated))
}

func TestAnnotateDoesNotDuplicateAnAlreadyListedRef(t *testing.T) {
	ref := blob.Ref{Digest: blob.DigestOf([]byte("a")), Ext: "png"}

	metadata, _ := json.Marshal(map[string]any{
		"observations": logWith(ref),
		"photo_uris":   []string{ref.Path()},
	})
	annotated, _, err := annotateBlobRefs(metadata)
	require.NoError(t, err)
	assert.Equal(t, []string{ref.Path()}, photoURIs(t, annotated))
}

func TestAnnotateLeavesRecordsWithoutImagesUntouched(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
	}{
		{"no metadata", ""},
		{"empty object", `{}`},
		{"log without images", `{"observations":"1. Powered on the rig, all lights green"}`},
		{"no log at all", `{"tags":["manual"],"notes":"fine"}`},
		// Fields belonging to some other client are not this function's to
		// interpret, let alone rewrite.
		{"observations is not a string", `{"observations":{"step":1}}`},
		{"metadata is not an object", `["not","an","object"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotated, refs, err := annotateBlobRefs(json.RawMessage(tt.metadata))
			require.NoError(t, err)
			assert.Empty(t, refs)
			if tt.metadata == "" {
				assert.JSONEq(t, `{}`, string(annotated))
			} else {
				assert.JSONEq(t, tt.metadata, string(annotated))
			}
		})
	}
}

// A photo_uris that is not a list of strings belongs to another client. The
// references are still recorded — the blobs are reachable and must not be swept
// — but nothing is written over the field.
func TestAnnotateKeepsRefsWhenPhotoURIsIsForeign(t *testing.T) {
	ref := blob.Ref{Digest: blob.DigestOf([]byte("a")), Ext: "png"}

	metadata, _ := json.Marshal(map[string]any{
		"observations": logWith(ref),
		"photo_uris":   map[string]string{"front": "https://example.com/x.jpg"},
	})
	annotated, refs, err := annotateBlobRefs(metadata)
	require.NoError(t, err)

	assert.Equal(t, []blob.Ref{ref}, refs)
	assert.JSONEq(t, string(metadata), string(annotated))
}
