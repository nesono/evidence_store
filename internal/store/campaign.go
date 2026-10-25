package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/campaign"
)

type CampaignPackage struct {
	ID          uuid.UUID         `json:"id"`
	Digest      string            `json:"digest"`
	ArchiveSize int64             `json:"archive_size"`
	ImportedAt  time.Time         `json:"imported_at"`
	ImportedBy  string            `json:"imported_by"`
	Definition  *campaign.Package `json:"definition"`
}

type CampaignSummary struct {
	ID            uuid.UUID         `json:"id"`
	Digest        string            `json:"digest"`
	ArchiveSize   int64             `json:"archive_size"`
	ImportedAt    time.Time         `json:"imported_at"`
	ImportedBy    string            `json:"imported_by"`
	Manifest      campaign.Manifest `json:"manifest"`
	ScenarioCount int               `json:"scenario_count"`
}

type CampaignStore struct{ pool *pgxpool.Pool }

func NewCampaignStore(pool *pgxpool.Pool) *CampaignStore { return &CampaignStore{pool: pool} }

// Import publishes metadata and reachability roots in one transaction, after
// writing every object to CAS. A failed write/commit leaves no visible campaign;
// any unreferenced objects are reclaimed by the normal orphan sweep.
// The caller has already validated the archive and extracted its declared assets.
func (s *CampaignStore) Import(ctx context.Context, blobs blob.Store, archive []byte, p *campaign.Package, assets map[string][]byte, subject string) (*CampaignPackage, bool, error) {
	if string(blob.DigestOf(archive)) != p.Digest {
		return nil, false, fmt.Errorf("archive digest does not match validated package")
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	// Shared with other imports, exclusive against the sweep's reachability
	// check + delete. This covers pre-existing orphan objects too: CAS Put does
	// not refresh their age, so the usual upload grace period is insufficient.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, campaignSweepLock); err != nil {
		return nil, false, err
	}
	if err := putCampaignObjects(ctx, blobs, archive, p.Digest, assets); err != nil {
		return nil, false, err
	}
	row, created, err := insertCampaign(ctx, tx, p.Digest, len(archive), subject, encoded)
	if err != nil {
		return nil, false, err
	}
	if err := insertCampaignRefs(ctx, tx, row.ID, p.Digest, assets); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return row, created, nil
}

func putCampaignObjects(ctx context.Context, blobs blob.Store, archive []byte, digest string, assets map[string][]byte) error {
	if err := putCampaignObject(ctx, blobs, digest, archive); err != nil {
		return err
	}
	for expected, data := range assets {
		if err := putCampaignObject(ctx, blobs, expected, data); err != nil {
			return err
		}
	}
	return nil
}

func putCampaignObject(ctx context.Context, blobs blob.Store, expected string, data []byte) error {
	if string(blob.DigestOf(data)) != expected {
		return fmt.Errorf("campaign object digest mismatch")
	}
	actual, size, err := blobs.Put(ctx, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("store campaign object: %w", err)
	}
	if string(actual) != expected || size != int64(len(data)) {
		return fmt.Errorf("CAS returned inconsistent campaign object identity")
	}
	return nil
}

const campaignColumns = `id, digest, archive_size, imported_at, imported_by, definition`

func insertCampaign(ctx context.Context, tx pgx.Tx, digest string, size int, subject string, definition []byte) (*CampaignPackage, bool, error) {
	row, err := scanCampaign(tx.QueryRow(ctx, `
		INSERT INTO campaign_package (digest, archive_size, imported_by, definition)
		VALUES ($1, $2, $3, $4) ON CONFLICT (digest) DO NOTHING
		RETURNING `+campaignColumns, digest, size, subject, definition))
	if !errors.Is(err, pgx.ErrNoRows) {
		return row, err == nil, err
	}
	// A separate statement sees the winner after a concurrent insert commits.
	row, err = scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaign_package WHERE digest = $1`, digest))
	return row, false, err
}

func insertCampaignRefs(ctx context.Context, tx pgx.Tx, id uuid.UUID, archive string, assets map[string][]byte) error {
	digests := []string{archive}
	for digest := range assets {
		digests = append(digests, digest)
	}
	slices.Sort(digests)
	for _, digest := range slices.Compact(digests) {
		if _, err := tx.Exec(ctx, `INSERT INTO campaign_blob_ref (campaign_id, digest) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, digest); err != nil {
			return err
		}
	}
	return nil
}

func scanCampaign(row pgx.Row) (*CampaignPackage, error) {
	var c CampaignPackage
	var definition []byte
	if err := row.Scan(&c.ID, &c.Digest, &c.ArchiveSize, &c.ImportedAt, &c.ImportedBy, &definition); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(definition, &c.Definition); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *CampaignStore) Get(ctx context.Context, id uuid.UUID) (*CampaignPackage, error) {
	return scanCampaign(s.pool.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaign_package WHERE id = $1`, id))
}

func (s *CampaignStore) List(ctx context.Context, limit, offset int) ([]CampaignSummary, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, digest, archive_size, imported_at, imported_by,
		definition->'manifest', jsonb_array_length(definition->'scenarios')
		FROM campaign_package ORDER BY imported_at, id LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CampaignSummary{}
	for rows.Next() {
		var c CampaignSummary
		var manifest []byte
		if err := rows.Scan(&c.ID, &c.Digest, &c.ArchiveSize, &c.ImportedAt, &c.ImportedBy, &manifest, &c.ScenarioCount); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(manifest, &c.Manifest); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
