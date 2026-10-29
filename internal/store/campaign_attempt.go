package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/campaign"
)

var ErrAttemptConflict = errors.New("attempt changed or request identity was reused")

type CampaignAttempt struct {
	ID                uuid.UUID          `json:"id"`
	CampaignID        uuid.UUID          `json:"campaign_id"`
	ProcedureRef      string             `json:"procedure_ref"`
	Owner             string             `json:"owner"`
	Revision          int64              `json:"revision"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	Data              campaign.DraftData `json:"data"`
	FinalizedAt       *time.Time         `json:"finalized_at,omitempty"`
	FinishedAt        *time.Time         `json:"finished_at,omitempty"`
	FinalizedRevision *int64             `json:"finalized_revision,omitempty"`
	EvidenceID        *uuid.UUID         `json:"evidence_id,omitempty"`
}

const attemptColumns = `id, campaign_id, procedure_ref, owner, revision, created_at, updated_at, data, finalized_at, finished_at, finalized_revision, evidence_id`

func scanAttempt(row pgx.Row) (*CampaignAttempt, error) {
	var a CampaignAttempt
	var data []byte
	if err := row.Scan(&a.ID, &a.CampaignID, &a.ProcedureRef, &a.Owner, &a.Revision, &a.CreatedAt, &a.UpdatedAt, &data, &a.FinalizedAt, &a.FinishedAt, &a.FinalizedRevision, &a.EvidenceID); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &a.Data); err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *CampaignStore) CreateAttempt(ctx context.Context, id, packageID uuid.UUID, ref, owner string, data campaign.DraftData) (*CampaignAttempt, bool, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, false, err
	}
	a, err := scanAttempt(s.pool.QueryRow(ctx, `INSERT INTO campaign_attempt (id,campaign_id,procedure_ref,owner,data)
		VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO NOTHING RETURNING `+attemptColumns, id, packageID, ref, owner, encoded))
	if !errors.Is(err, pgx.ErrNoRows) {
		return a, err == nil, err
	}
	a, err = s.GetAttempt(ctx, id, owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrAttemptConflict
	}
	if err != nil {
		return nil, false, err
	}
	if a.CampaignID != packageID || a.ProcedureRef != ref {
		return nil, false, ErrAttemptConflict
	}
	return a, false, nil
}

func (s *CampaignStore) GetAttempt(ctx context.Context, id uuid.UUID, owner string) (*CampaignAttempt, error) {
	return scanAttempt(s.pool.QueryRow(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE id=$1 AND owner=$2`, id, owner))
}

// SaveAttempt serializes edits on one row. Retry of the last accepted operation
// succeeds only for the same base revision and data; a conflicting edit is never
// silently overwritten. All identity and ownership columns remain immutable.
func (s *CampaignStore) SaveAttempt(ctx context.Context, id uuid.UUID, owner string, revision int64, operation uuid.UUID, data campaign.DraftData, blobs ...blob.Store) (*CampaignAttempt, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, campaignSweepLock); err != nil {
		return nil, err
	}
	a, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE id=$1 AND owner=$2 FOR UPDATE`, id, owner))
	if err != nil {
		return nil, err
	}
	if a.FinalizedAt != nil {
		return nil, ErrAttemptConflict
	}
	var sameOperation, samePayload bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(last_operation_id=$2,false),
		COALESCE(last_base_revision=$3 AND data=$4::jsonb,false) FROM campaign_attempt WHERE id=$1`, id, operation, revision, encoded).Scan(&sameOperation, &samePayload)
	if err != nil {
		return nil, err
	}
	if sameOperation {
		if !samePayload {
			return nil, ErrAttemptConflict
		}
		return a, tx.Commit(ctx)
	}
	if a.Revision != revision {
		return nil, ErrAttemptConflict
	}
	if err := replaceDraftBlobRefs(ctx, tx, id, data.Metadata, blobs); err != nil {
		return nil, err
	}
	a, err = scanAttempt(tx.QueryRow(ctx, `UPDATE campaign_attempt SET data=$2, revision=revision+1, updated_at=now(),
		last_operation_id=$3, last_base_revision=$4 WHERE id=$1 RETURNING `+attemptColumns, id, encoded, operation, revision))
	if err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
}

func replaceDraftBlobRefs(ctx context.Context, tx pgx.Tx, id uuid.UUID, metadata map[string]json.RawMessage, blobs []blob.Store) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, refs, err := annotateBlobRefs(encoded)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if len(blobs) == 0 || blobs[0] == nil {
			return &FinalizationError{"attachment storage is unavailable"}
		}
		if _, err := blobs[0].Stat(ctx, ref.Digest); err != nil {
			if errors.Is(err, blob.ErrNotFound) {
				return &FinalizationError{"test log references a missing attachment; upload it before saving"}
			}
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM campaign_draft_blob_ref WHERE attempt_id=$1`, id); err != nil {
		return err
	}
	for _, ref := range refs {
		if _, err := tx.Exec(ctx, `INSERT INTO campaign_draft_blob_ref(attempt_id,digest) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, ref.Digest); err != nil {
			return err
		}
	}
	return nil
}

// Software context is reusable across procedures, but scoped to this exact
// package and owner. New untouched drafts and expired results are not defaults.
func (s *CampaignStore) LatestSoftwareContext(ctx context.Context, packageID uuid.UUID, owner string) (campaign.DraftData, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM campaign_attempt WHERE campaign_id=$1 AND owner=$2
		AND revision>1 AND COALESCE(data->>'repo','')<>'' AND COALESCE(data->>'rcs_ref','')<>''
		ORDER BY updated_at DESC,id DESC LIMIT 1`, packageID, owner).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return campaign.DraftData{}, nil
	}
	if err != nil {
		return campaign.DraftData{}, err
	}
	var prior campaign.DraftData
	if err := json.Unmarshal(data, &prior); err != nil {
		return campaign.DraftData{}, err
	}
	return campaign.DraftData{Repo: prior.Repo, Branch: prior.Branch, RCSRef: prior.RCSRef}, nil
}

func (s *CampaignStore) ListAttempts(ctx context.Context, packageID uuid.UUID, ref, owner string, offset int) ([]CampaignAttempt, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt
		WHERE campaign_id=$1 AND procedure_ref=$2 AND owner=$3 ORDER BY created_at DESC,id DESC LIMIT 26 OFFSET $4`, packageID, ref, owner, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CampaignAttempt{}
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, *a)
	}
	return result, rows.Err()
}
