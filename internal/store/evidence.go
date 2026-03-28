package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iss/evidence-store/internal/model"
)

type EvidenceStore struct {
	pool *pgxpool.Pool
}

func NewEvidenceStore(pool *pgxpool.Pool) *EvidenceStore {
	return &EvidenceStore{pool: pool}
}

func (s *EvidenceStore) Insert(ctx context.Context, e *model.EvidenceCreate) (*model.Evidence, error) {
	metadata := e.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO evidence (repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, ingested_at, metadata
	`, e.Repo, e.Branch, e.RCSRef, e.ProcedureRef, e.EvidenceType, e.Source, e.Result, e.FinishedAt, metadata)

	return scanEvidence(row)
}

func (s *EvidenceStore) InsertBatch(ctx context.Context, records []model.EvidenceCreate) ([]model.Evidence, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	results := make([]model.Evidence, 0, len(records))
	for _, e := range records {
		metadata := e.Metadata
		if metadata == nil {
			metadata = json.RawMessage(`{}`)
		}

		row := tx.QueryRow(ctx, `
			INSERT INTO evidence (repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id, repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, ingested_at, metadata
		`, e.Repo, e.Branch, e.RCSRef, e.ProcedureRef, e.EvidenceType, e.Source, e.Result, e.FinishedAt, metadata)

		ev, err := scanEvidence(row)
		if err != nil {
			return nil, fmt.Errorf("insert record: %w", err)
		}
		results = append(results, *ev)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return results, nil
}

func (s *EvidenceStore) GetByID(ctx context.Context, id uuid.UUID) (*model.Evidence, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, ingested_at, metadata
		FROM evidence
		WHERE id = $1
	`, id)

	return scanEvidence(row)
}

type ListParams struct {
	Filter model.EvidenceFilter
	Cursor *Cursor
	Limit  int
}

type ListResult struct {
	Records    []model.Evidence `json:"records"`
	NextCursor *string          `json:"next_cursor,omitempty"`
}

func (s *EvidenceStore) List(ctx context.Context, params ListParams) (*ListResult, error) {
	var (
		where []string
		args  []any
		argN  = 1
	)

	arg := func(v any) string {
		args = append(args, v)
		s := fmt.Sprintf("$%d", argN)
		argN++
		return s
	}

	if v := params.Filter.Repo; v != nil {
		where = append(where, fmt.Sprintf("repo = %s", arg(*v)))
	}
	if v := params.Filter.RCSRef; v != nil {
		where = append(where, fmt.Sprintf("rcs_ref = %s", arg(*v)))
	}
	if v := params.Filter.Branch; v != nil {
		where = append(where, fmt.Sprintf("branch = %s", arg(*v)))
	}
	if v := params.Filter.EvidenceType; v != nil {
		where = append(where, fmt.Sprintf("evidence_type = %s", arg(*v)))
	}
	if v := params.Filter.Source; v != nil {
		where = append(where, fmt.Sprintf("source = %s", arg(*v)))
	}
	if v := params.Filter.ProcedureRef; v != nil {
		if strings.HasSuffix(*v, "*") {
			prefix := strings.TrimSuffix(*v, "*")
			where = append(where, fmt.Sprintf("procedure_ref LIKE %s", arg(prefix+"%")))
		} else {
			where = append(where, fmt.Sprintf("procedure_ref = %s", arg(*v)))
		}
	}
	if len(params.Filter.Result) > 0 {
		placeholders := make([]string, len(params.Filter.Result))
		for i, r := range params.Filter.Result {
			placeholders[i] = arg(r)
		}
		where = append(where, fmt.Sprintf("result IN (%s)", strings.Join(placeholders, ",")))
	}
	if v := params.Filter.FinishedAfter; v != nil {
		where = append(where, fmt.Sprintf("finished_at >= %s", arg(*v)))
	}
	if v := params.Filter.FinishedBefore; v != nil {
		where = append(where, fmt.Sprintf("finished_at < %s", arg(*v)))
	}
	if len(params.Filter.Tags) > 0 {
		where = append(where, fmt.Sprintf("metadata->'tags' @> %s", arg(mustJSON(params.Filter.Tags))))
	}
	if params.Cursor != nil {
		where = append(where, fmt.Sprintf("(ingested_at, id) > (%s, %s)", arg(params.Cursor.IngestedAt), arg(params.Cursor.ID)))
	}

	query := "SELECT id, repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, ingested_at, metadata FROM evidence"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY ingested_at ASC, id ASC"
	query += fmt.Sprintf(" LIMIT %s", arg(params.Limit+1))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query evidence: %w", err)
	}
	defer rows.Close()

	var records []model.Evidence
	for rows.Next() {
		ev, err := scanEvidenceRow(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence rows: %w", err)
	}

	result := &ListResult{}
	if len(records) > params.Limit {
		last := records[params.Limit-1]
		cursor := EncodeCursor(last.IngestedAt, last.ID)
		result.NextCursor = &cursor
		records = records[:params.Limit]
	}
	result.Records = records

	return result, nil
}

func scanEvidence(row pgx.Row) (*model.Evidence, error) {
	var e model.Evidence
	err := row.Scan(
		&e.ID, &e.Repo, &e.Branch, &e.RCSRef, &e.ProcedureRef,
		&e.EvidenceType, &e.Source, &e.Result, &e.FinishedAt, &e.IngestedAt, &e.Metadata,
	)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func scanEvidenceRow(rows pgx.Rows) (*model.Evidence, error) {
	var e model.Evidence
	err := rows.Scan(
		&e.ID, &e.Repo, &e.Branch, &e.RCSRef, &e.ProcedureRef,
		&e.EvidenceType, &e.Source, &e.Result, &e.FinishedAt, &e.IngestedAt, &e.Metadata,
	)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
