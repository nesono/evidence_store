package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/model"
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

// DeleteBatch deletes evidence records by IDs and returns the number of rows deleted.
func (s *EvidenceStore) DeleteBatch(ctx context.Context, ids []uuid.UUID) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM evidence WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("delete evidence batch: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ScanAll iterates over all evidence records ordered by finished_at ASC in batches,
// calling fn for each batch. Stops if fn returns an error.
func (s *EvidenceStore) ScanAll(ctx context.Context, batchSize int, fn func([]model.Evidence) error) error {
	var lastFinishedAt *string
	var lastID *uuid.UUID

	for {
		var where string
		var args []any

		if lastFinishedAt != nil && lastID != nil {
			where = " WHERE (finished_at, id) > ($1, $2)"
			args = []any{*lastFinishedAt, *lastID}
		}

		query := fmt.Sprintf(
			"SELECT id, repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, ingested_at, metadata FROM evidence%s ORDER BY finished_at ASC, id ASC LIMIT %d",
			where, batchSize,
		)

		rows, err := s.pool.Query(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("scan evidence: %w", err)
		}

		var batch []model.Evidence
		for rows.Next() {
			ev, err := scanEvidenceRow(rows)
			if err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, *ev)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("scan evidence rows: %w", err)
		}

		if len(batch) == 0 {
			break
		}

		if err := fn(batch); err != nil {
			return err
		}

		last := batch[len(batch)-1]
		ts := last.FinishedAt.Format("2006-01-02T15:04:05.999999Z07:00")
		lastFinishedAt = &ts
		lastID = &last.ID

		if len(batch) < batchSize {
			break
		}
	}

	return nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
