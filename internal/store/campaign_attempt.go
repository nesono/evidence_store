package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/campaign"
)

var ErrAttemptConflict = errors.New("attempt changed or request identity was reused")

type CampaignAttempt struct {
	ID           uuid.UUID          `json:"id"`
	CampaignID   uuid.UUID          `json:"campaign_id"`
	ProcedureRef string             `json:"procedure_ref"`
	Owner        string             `json:"owner"`
	Revision     int64              `json:"revision"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
	Data         campaign.DraftData `json:"data"`
}

const attemptColumns = `id, campaign_id, procedure_ref, owner, revision, created_at, updated_at, data`

func scanAttempt(row pgx.Row) (*CampaignAttempt, error) {
	var a CampaignAttempt
	var data []byte
	if err := row.Scan(&a.ID, &a.CampaignID, &a.ProcedureRef, &a.Owner, &a.Revision, &a.CreatedAt, &a.UpdatedAt, &data); err != nil {
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
func (s *CampaignStore) SaveAttempt(ctx context.Context, id uuid.UUID, owner string, revision int64, operation uuid.UUID, data campaign.DraftData) (*CampaignAttempt, error) {
	encoded, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	a, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE id=$1 AND owner=$2 FOR UPDATE`, id, owner))
	if err != nil {
		return nil, err
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
	a, err = scanAttempt(tx.QueryRow(ctx, `UPDATE campaign_attempt SET data=$2, revision=revision+1, updated_at=now(),
		last_operation_id=$3, last_base_revision=$4 WHERE id=$1 RETURNING `+attemptColumns, id, encoded, operation, revision))
	if err != nil {
		return nil, err
	}
	return a, tx.Commit(ctx)
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
