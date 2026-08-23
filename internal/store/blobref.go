package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/blob"
)

// BlobRefStore answers which blobs are still reachable from evidence. It is the
// half of blob lifetime that lives in Postgres; the bytes are the object
// store's business.
type BlobRefStore struct {
	pool *pgxpool.Pool
}

func NewBlobRefStore(pool *pgxpool.Pool) *BlobRefStore {
	return &BlobRefStore{pool: pool}
}

// Referenced returns the subset of digests some evidence record still points
// at. The sweep asks in batches rather than fetching every reference, because
// the number of stored objects is the thing that grows.
func (s *BlobRefStore) Referenced(ctx context.Context, digests []blob.Digest) (map[blob.Digest]bool, error) {
	referenced := make(map[blob.Digest]bool, len(digests))
	if len(digests) == 0 {
		return referenced, nil
	}

	values := make([]string, len(digests))
	for i, d := range digests {
		values[i] = string(d)
	}

	rows, err := s.pool.Query(ctx, `SELECT DISTINCT digest FROM blob_ref WHERE digest = ANY($1)`, values)
	if err != nil {
		return nil, fmt.Errorf("query blob refs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("scan blob ref: %w", err)
		}
		referenced[blob.Digest(d)] = true
	}
	return referenced, rows.Err()
}

// ForEvidence returns the digests one record references.
func (s *BlobRefStore) ForEvidence(ctx context.Context, id uuid.UUID) ([]blob.Digest, error) {
	rows, err := s.pool.Query(ctx, `SELECT digest FROM blob_ref WHERE evidence_id = $1 ORDER BY digest`, id)
	if err != nil {
		return nil, fmt.Errorf("query blob refs: %w", err)
	}
	defer rows.Close()

	var digests []blob.Digest
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("scan blob ref: %w", err)
		}
		digests = append(digests, blob.Digest(d))
	}
	return digests, rows.Err()
}

// recordBlobRefs writes one row per blob a record references. It runs inside
// the insert's transaction: a record that mentions a blob without a reference
// row would have its images swept out from under it.
func recordBlobRefs(ctx context.Context, tx pgx.Tx, evidenceID uuid.UUID, refs []blob.Ref) error {
	for _, ref := range refs {
		// A log that names the same blob twice, or two records naming one blob,
		// are both ordinary. The primary key makes the second write a no-op.
		_, err := tx.Exec(ctx, `
			INSERT INTO blob_ref (digest, evidence_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, string(ref.Digest), evidenceID)
		if err != nil {
			return fmt.Errorf("record blob ref: %w", err)
		}
	}
	return nil
}

// annotateBlobRefs finds the blobs a record's test log references and lists
// them under metadata.photo_uris.
//
// The log is the source of truth — it is where a tester actually put the image —
// but a client reading the API should not have to parse markdown to find out
// that a record has photos attached. photo_uris is the field DESIGN.md §2.2
// already reserves for exactly that, so it is filled in from the log rather
// than being a second thing to keep in sync by hand.
//
// Entries a client supplied itself are kept: this adds, it does not replace.
func annotateBlobRefs(metadata json.RawMessage) (json.RawMessage, []blob.Ref, error) {
	if len(metadata) == 0 {
		return json.RawMessage(`{}`), nil, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil {
		// Metadata that is not an object is the validator's problem, not this
		// function's. Passing it through unchanged keeps the error where it is
		// already reported.
		return metadata, nil, nil
	}

	var observations string
	if raw, ok := fields["observations"]; ok {
		// Anything but a string under `observations` belongs to some other
		// client and is left alone.
		if err := json.Unmarshal(raw, &observations); err != nil {
			return metadata, nil, nil
		}
	}

	refs := blob.Refs(observations)
	if len(refs) == 0 {
		return metadata, nil, nil
	}

	var existing []string
	if raw, ok := fields["photo_uris"]; ok {
		if err := json.Unmarshal(raw, &existing); err != nil {
			// Same reasoning: a photo_uris that is not a list of strings is not
			// ours to rewrite.
			return metadata, refs, nil
		}
	}

	have := make(map[string]bool, len(existing))
	for _, uri := range existing {
		have[uri] = true
	}
	uris := existing
	for _, ref := range refs {
		if path := ref.Path(); !have[path] {
			uris = append(uris, path)
			have[path] = true
		}
	}

	encoded, err := json.Marshal(uris)
	if err != nil {
		return nil, nil, fmt.Errorf("encode photo_uris: %w", err)
	}
	fields["photo_uris"] = encoded

	annotated, err := json.Marshal(fields)
	if err != nil {
		return nil, nil, fmt.Errorf("encode metadata: %w", err)
	}
	return annotated, refs, nil
}
