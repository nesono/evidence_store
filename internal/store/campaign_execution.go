package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/campaign"
)

var ErrCampaignConflict = errors.New("campaign changed or is closed; reload the campaign")

type CampaignExecution struct {
	ID         uuid.UUID                   `json:"id"`
	PackageID  uuid.UUID                   `json:"package_id"`
	Name       string                      `json:"name"`
	Repo       string                      `json:"repo"`
	Branch     string                      `json:"branch"`
	RCSRef     string                      `json:"rcs_ref"`
	CreatedBy  string                      `json:"created_by"`
	CreatedAt  time.Time                   `json:"created_at"`
	UpdatedAt  time.Time                   `json:"updated_at"`
	Revision   int64                       `json:"revision"`
	ClosedAt   *time.Time                  `json:"closed_at,omitempty"`
	ClosedBy   *string                     `json:"closed_by,omitempty"`
	Procedures []CampaignProcedureProgress `json:"procedures"`
}

type CampaignProcedureProgress struct {
	ProcedureRef string     `json:"procedure_ref"`
	Title        string     `json:"title"`
	Attempts     int        `json:"attempts"`
	DraftID      *uuid.UUID `json:"draft_id,omitempty"`
	LatestID     *uuid.UUID `json:"latest_id,omitempty"`
	Result       string     `json:"result"`
}

const executionColumns = `id,package_id,name,repo,branch,rcs_ref,created_by,created_at,updated_at,revision,closed_at,closed_by`

func scanExecution(row pgx.Row) (*CampaignExecution, error) {
	var e CampaignExecution
	err := row.Scan(&e.ID, &e.PackageID, &e.Name, &e.Repo, &e.Branch, &e.RCSRef, &e.CreatedBy, &e.CreatedAt, &e.UpdatedAt, &e.Revision, &e.ClosedAt, &e.ClosedBy)
	return &e, err
}

func (s *CampaignStore) CreateExecution(ctx context.Context, e CampaignExecution) (*CampaignExecution, bool, error) {
	if e.ID == uuid.Nil || e.PackageID == uuid.Nil || strings.TrimSpace(e.Name) == "" || len(e.Name) > 200 || strings.TrimSpace(e.Repo) == "" || strings.TrimSpace(e.Branch) == "" || strings.TrimSpace(e.RCSRef) == "" {
		return nil, false, &FinalizationError{"campaign id, package, name (up to 200 bytes), repository, branch and revision are required"}
	}
	if len(e.Repo) > 1000 || len(e.Branch) > 1000 || len(e.RCSRef) > 1000 {
		return nil, false, &FinalizationError{"product context fields must be at most 1000 bytes"}
	}
	if _, err := s.Get(ctx, e.PackageID); err != nil {
		return nil, false, err
	}
	row, err := scanExecution(s.pool.QueryRow(ctx, `INSERT INTO campaign_execution(id,package_id,name,repo,branch,rcs_ref,created_by)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING RETURNING `+executionColumns, e.ID, e.PackageID, e.Name, e.Repo, e.Branch, e.RCSRef, e.CreatedBy))
	if !errors.Is(err, pgx.ErrNoRows) {
		return row, err == nil, err
	}
	row, err = scanExecution(s.pool.QueryRow(ctx, `SELECT `+executionColumns+` FROM campaign_execution WHERE id=$1`, e.ID))
	if err != nil {
		return nil, false, err
	}
	if row.PackageID != e.PackageID || row.Name != e.Name || row.Repo != e.Repo || row.Branch != e.Branch || row.RCSRef != e.RCSRef || row.CreatedBy != e.CreatedBy {
		return nil, false, ErrCampaignConflict
	}
	return row, false, nil
}

// Literal subsequence matching supports abbreviations without treating SQL
// wildcard characters typed by the user as wildcard operators.
func fuzzyPattern(text string) string {
	var b strings.Builder
	b.WriteByte('%')
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		if unicode.IsSpace(r) {
			continue
		}
		if r == '%' || r == '_' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
		b.WriteByte('%')
	}
	return b.String()
}

func (s *CampaignStore) ListExecutions(ctx context.Context, state, query string, limit, offset int) ([]CampaignExecution, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+executionColumns+` FROM campaign_execution e
 WHERE ($1='all' OR ($1='open' AND closed_at IS NULL) OR ($1='completed' AND closed_at IS NOT NULL))
 AND lower(name||' '||repo||' '||branch||' '||rcs_ref||' '||created_by||' '||
 (SELECT definition->'manifest'->'campaign'->>'name' FROM campaign_package WHERE id=e.package_id)) LIKE $2
 ORDER BY updated_at DESC,id DESC LIMIT $3 OFFSET $4`, state, fuzzyPattern(query), limit, offset)
	if err != nil {
		return nil, err
	}
	result := []CampaignExecution{}
	for rows.Next() {
		e, err := scanExecution(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, *e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		if err := s.executionProgress(ctx, &result[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *CampaignStore) GetExecution(ctx context.Context, id uuid.UUID) (*CampaignExecution, error) {
	// Lifecycle revision and progress must describe the same snapshot; closure
	// confirmation uses both to avoid skipping work the caller has not seen.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	e, err := scanExecution(tx.QueryRow(ctx, `SELECT `+executionColumns+` FROM campaign_execution WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	if err := executionProgress(ctx, tx, e); err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

func (s *CampaignStore) executionProgress(ctx context.Context, e *CampaignExecution) error {
	return executionProgress(ctx, s.pool, e)
}

func executionProgress(ctx context.Context, q blobRefQuerier, e *CampaignExecution) error {
	rows, err := q.Query(ctx, `SELECT d->'definition'->>'procedure_ref',d->'definition'->>'title',
 (SELECT count(*) FROM campaign_attempt WHERE execution_id=$1 AND procedure_ref=d->'definition'->>'procedure_ref'),
 (SELECT id FROM campaign_attempt WHERE execution_id=$1 AND procedure_ref=d->'definition'->>'procedure_ref' AND finalized_at IS NULL),
 latest.id, COALESCE(latest.result,'')
 FROM campaign_package p CROSS JOIN LATERAL jsonb_array_elements(p.definition->'scenarios') WITH ORDINALITY AS defs(d,n)
 LEFT JOIN LATERAL (SELECT id,data->>'result' AS result FROM campaign_attempt
 WHERE execution_id=$1 AND procedure_ref=d->'definition'->>'procedure_ref' AND finalized_at IS NOT NULL
 ORDER BY created_at DESC,id DESC LIMIT 1) latest ON true
 WHERE p.id=$2 ORDER BY n`, e.ID, e.PackageID)
	if err != nil {
		return err
	}
	defer rows.Close()
	e.Procedures = []CampaignProcedureProgress{}
	for rows.Next() {
		var p CampaignProcedureProgress
		if err := rows.Scan(&p.ProcedureRef, &p.Title, &p.Attempts, &p.DraftID, &p.LatestID, &p.Result); err != nil {
			return err
		}
		e.Procedures = append(e.Procedures, p)
	}
	return rows.Err()
}

// Lock the execution before its attempts in every mutation, including closure.
// This prevents a save/retry from racing the close-to-skip transaction.
func lockAttemptExecution(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*CampaignExecution, error) {
	var executionID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT execution_id FROM campaign_attempt WHERE id=$1`, id).Scan(&executionID); err != nil {
		return nil, err
	}
	if executionID == nil {
		return nil, nil
	}
	return scanExecution(tx.QueryRow(ctx, `SELECT `+executionColumns+` FROM campaign_execution WHERE id=$1 FOR UPDATE`, executionID))
}

func touchExecution(ctx context.Context, tx pgx.Tx, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE campaign_execution SET revision=revision+1,updated_at=now() WHERE id=$1`, id)
	return err
}

func (s *CampaignStore) StartExecutionAttempt(ctx context.Context, executionID, id uuid.UUID, ref, actor string) (*CampaignAttempt, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx)
	e, err := scanExecution(tx.QueryRow(ctx, `SELECT `+executionColumns+` FROM campaign_execution WHERE id=$1 FOR UPDATE`, executionID))
	if err != nil {
		return nil, false, err
	}
	prior, err := scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE id=$1`, id))
	if err == nil {
		if prior.ExecutionID == nil || *prior.ExecutionID != executionID || prior.ProcedureRef != ref {
			return nil, false, ErrAttemptConflict
		}
		return prior, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	if e.ClosedAt != nil {
		return nil, false, ErrCampaignConflict
	}
	prior, err = scanAttempt(tx.QueryRow(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE execution_id=$1 AND procedure_ref=$2 AND finalized_at IS NULL`, executionID, ref))
	if err == nil {
		return prior, false, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	p, err := scanCampaign(tx.QueryRow(ctx, `SELECT `+campaignColumns+` FROM campaign_package WHERE id=$1`, e.PackageID))
	if err != nil {
		return nil, false, err
	}
	scenario, _, err := finalScenario(p, ref)
	if err != nil {
		return nil, false, &FinalizationError{"procedure is not in this campaign package"}
	}
	data := campaign.InitialDraft(scenario.Procedure)
	data.Repo, data.Branch, data.RCSRef = e.Repo, e.Branch, e.RCSRef
	data.Source = actor
	data.EvidenceType = scenario.Definition.Defaults.EvidenceType
	data.Metadata = map[string]json.RawMessage{}
	for key, value := range scenario.Definition.Defaults.Metadata {
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, false, err
		}
		data.Metadata[key] = raw
	}
	data.Metadata["tags"], err = json.Marshal(scenario.Definition.Defaults.Tags)
	if err != nil {
		return nil, false, err
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, false, err
	}
	a, err := scanAttempt(tx.QueryRow(ctx, `INSERT INTO campaign_attempt(id,campaign_id,execution_id,procedure_ref,owner,updated_by,data,created_at)
	 VALUES($1,$2,$3,$4,$5,$5,$6,clock_timestamp()) RETURNING `+attemptColumns, id, e.PackageID, e.ID, ref, actor, raw))
	if err != nil {
		return nil, false, err
	}
	if err := touchExecution(ctx, tx, &e.ID); err != nil {
		return nil, false, err
	}
	return a, true, tx.Commit(ctx)
}

func (s *CampaignStore) ListExecutionAttempts(ctx context.Context, id uuid.UUID, ref string, offset int) ([]CampaignAttempt, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+attemptColumns+` FROM campaign_attempt WHERE execution_id=$1 AND procedure_ref=$2 ORDER BY created_at DESC,id DESC LIMIT 26 OFFSET $3`, id, ref, offset)
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
