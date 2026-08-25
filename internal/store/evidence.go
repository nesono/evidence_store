package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/model"
)

// evidenceColumns is what every read of the table selects, in the order the
// scan helpers below expect. One list rather than a copy per query: a column
// added to the table has to reach the scan in the same order at every call
// site, and four copies of the list is four chances to miss one.
const evidenceColumns = "id, client_record_id, repo, branch, rcs_ref, procedure_ref, " +
	"evidence_type, source, result, finished_at, ingested_at, metadata"

type EvidenceStore struct {
	pool  *pgxpool.Pool
	stats *statsCache
}

func NewEvidenceStore(pool *pgxpool.Pool) *EvidenceStore {
	return NewEvidenceStoreWithCache(pool, 0)
}

// maxCachedAggregations bounds the cache. Analytics filters vary little in
// practice, so this is a guard against unbounded growth rather than a tuning
// knob.
const maxCachedAggregations = 64

// NewEvidenceStoreWithCache returns a store that reuses aggregation results for
// ttl. A ttl of zero disables caching.
func NewEvidenceStoreWithCache(pool *pgxpool.Pool, ttl time.Duration) *EvidenceStore {
	return &EvidenceStore{pool: pool, stats: newStatsCache(ttl, maxCachedAggregations)}
}

// InsertResult is what became of one submission: the record the store holds
// for it, and whether this call is what put it there. The two differ only for
// a client that sent a client_record_id the store had already seen.
type InsertResult struct {
	Evidence *model.Evidence
	Created  bool
}

func (s *EvidenceStore) Insert(ctx context.Context, e *model.EvidenceCreate) (*InsertResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	res, err := insertOne(ctx, tx, e)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return res, nil
}

func (s *EvidenceStore) InsertBatch(ctx context.Context, records []model.EvidenceCreate) ([]InsertResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	results := make([]InsertResult, 0, len(records))
	for _, e := range records {
		// One transaction for the whole batch, which is also what makes a
		// token repeated inside a batch resolve the same way as one repeated
		// across two calls: the unique index sees the earlier row immediately,
		// committed or not.
		res, err := insertOne(ctx, tx, &e)
		if err != nil {
			return nil, fmt.Errorf("insert record: %w", err)
		}
		results = append(results, *res)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	return results, nil
}

// insertOne stores one record and the references to whatever blobs its test log
// points at. The two happen in one transaction because a record that mentions a
// blob without a reference row would have its images swept out from under it.
//
// A record carrying a client_record_id the store has already seen is not stored
// again. The existing record is returned instead, untouched — it is evidence,
// and a resend is a question about it, not a revision of it.
func insertOne(ctx context.Context, tx pgx.Tx, e *model.EvidenceCreate) (*InsertResult, error) {
	metadata, refs, err := annotateBlobRefs(e.Metadata)
	if err != nil {
		return nil, err
	}

	// Validation has already rejected a value that is not a UUID; this is the
	// conversion, and it fails only if something reached the store without
	// passing through it.
	var clientRecordID *uuid.UUID
	if e.ClientRecordID != nil {
		parsed, err := uuid.Parse(*e.ClientRecordID)
		if err != nil {
			return nil, fmt.Errorf("client_record_id %q: %w", *e.ClientRecordID, err)
		}
		clientRecordID = &parsed
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO evidence (client_record_id, repo, branch, rcs_ref, procedure_ref, evidence_type, source, result, finished_at, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		-- The predicate is what lets this infer the partial index. A record
		-- without a token has nothing to conflict with and always inserts.
		ON CONFLICT (client_record_id) WHERE client_record_id IS NOT NULL DO NOTHING
		RETURNING `+evidenceColumns, clientRecordID,
		e.Repo, e.Branch, e.RCSRef, e.ProcedureRef, e.EvidenceType, e.Source, e.Result, e.FinishedAt.UTC(), metadata)

	ev, err := scanEvidence(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// DO NOTHING returns no row, which is the store saying it already has
		// this submission. Which record that became is the useful answer, so
		// read it back rather than reporting a conflict the client can do
		// nothing with.
		if clientRecordID == nil {
			return nil, fmt.Errorf("insert returned no row for a record with no client_record_id")
		}
		existing, err := findByClientRecordID(ctx, tx, *clientRecordID)
		if err != nil {
			return nil, err
		}
		// Deliberately no recordBlobRefs: the existing record's references were
		// written when it was filed, and its test log has not changed.
		return &InsertResult{Evidence: existing, Created: false}, nil
	}
	if err != nil {
		return nil, err
	}

	if err := recordBlobRefs(ctx, tx, ev.ID, refs); err != nil {
		return nil, err
	}

	return &InsertResult{Evidence: ev, Created: true}, nil
}

// findByClientRecordID reads the record a submission already became. It runs on
// the same transaction as the insert that conflicted, so it also finds a row
// written earlier in the same batch.
func findByClientRecordID(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*model.Evidence, error) {
	row := tx.QueryRow(ctx, `SELECT `+evidenceColumns+` FROM evidence WHERE client_record_id = $1`, id)
	ev, err := scanEvidence(row)
	if err != nil {
		return nil, fmt.Errorf("read back record for client_record_id %s: %w", id, err)
	}
	return ev, nil
}

func (s *EvidenceStore) GetByID(ctx context.Context, id uuid.UUID) (*model.Evidence, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+evidenceColumns+` FROM evidence WHERE id = $1
	`, id)

	return scanEvidence(row)
}

type ListParams struct {
	Filter model.EvidenceFilter
	Cursor *Cursor
	Limit  int
	// Offset skips the first N matching rows. Only meaningful when Cursor is nil —
	// the two are different pagination modes and the API rejects their combination.
	Offset int
	// Sort names the column to order by; empty means the default (ingested_at ASC).
	// Must be a member of sortColumns.
	Sort string
	Desc bool
	// WithTotal requests a COUNT(*) of all matching rows. Callers paging through a
	// result set should set it only on the first request and cache the answer.
	WithTotal bool
}

// sortColumns are the columns that may be used as an ORDER BY key. Whitelisted
// because the column name is interpolated directly into the query, the same
// approach used by distinctFields.
var sortColumns = map[string]bool{
	"repo":          true,
	"branch":        true,
	"rcs_ref":       true,
	"procedure_ref": true,
	"evidence_type": true,
	"source":        true,
	"result":        true,
	"finished_at":   true,
	"ingested_at":   true,
}

// IsSortable reports whether column may be used as a sort key.
func IsSortable(column string) bool {
	return sortColumns[column]
}

type ListResult struct {
	Records    []model.Evidence `json:"records"`
	NextCursor *string          `json:"next_cursor,omitempty"`
	Total      *int64           `json:"total,omitempty"`
}

func (s *EvidenceStore) List(ctx context.Context, params ListParams) (*ListResult, error) {
	f := buildFilter(params.Filter)

	// Count matching records before adding pagination clauses. Only done when the
	// caller asks for it — pages after the first keep the total from the initial
	// call so we don't recount on every "next page" click.
	var total *int64
	if params.WithTotal {
		countQuery := "SELECT COUNT(*) FROM evidence" + f.whereClause()
		var n int64
		if err := s.pool.QueryRow(ctx, countQuery, f.args...).Scan(&n); err != nil {
			return nil, fmt.Errorf("count evidence: %w", err)
		}
		total = &n
	}

	if params.Cursor != nil {
		f.add(fmt.Sprintf("(ingested_at, id) > (%s, %s)", f.arg(params.Cursor.IngestedAt), f.arg(params.Cursor.ID)))
	}

	query := "SELECT " + evidenceColumns + " FROM evidence" + f.whereClause()

	// id breaks ties so the total order is deterministic and offset windows
	// neither repeat nor skip records.
	if params.Sort == "" {
		query += " ORDER BY ingested_at ASC, id ASC"
	} else {
		if !sortColumns[params.Sort] {
			return nil, fmt.Errorf("column %q is not sortable", params.Sort)
		}
		direction := "ASC"
		if params.Desc {
			direction = "DESC"
		}
		// The tie-break follows the sort direction so that descending is the exact
		// reverse of ascending. Clients rely on that to read a window near the end
		// of a large result set from the far end, avoiding a deep OFFSET scan.
		query += fmt.Sprintf(" ORDER BY %s %s, id %s", params.Sort, direction, direction)
	}

	query += fmt.Sprintf(" LIMIT %s", f.arg(params.Limit+1))
	if params.Offset > 0 && params.Cursor == nil {
		query += fmt.Sprintf(" OFFSET %s", f.arg(params.Offset))
	}

	rows, err := s.pool.Query(ctx, query, f.args...)
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

	result := &ListResult{Total: total}
	if len(records) > params.Limit {
		records = records[:params.Limit]
		// A keyset cursor encodes a position in the default (ingested_at, id)
		// ordering, so it is only meaningful when that ordering is in effect.
		// Under a custom sort, callers page with offset instead.
		if params.Sort == "" {
			last := records[params.Limit-1]
			cursor := EncodeCursor(last.IngestedAt, last.ID)
			result.NextCursor = &cursor
		}
	}
	result.Records = records

	return result, nil
}

func scanEvidence(row pgx.Row) (*model.Evidence, error) {
	var e model.Evidence
	err := row.Scan(
		&e.ID, &e.ClientRecordID, &e.Repo, &e.Branch, &e.RCSRef, &e.ProcedureRef,
		&e.EvidenceType, &e.Source, &e.Result, &e.FinishedAt, &e.IngestedAt, &e.Metadata,
	)
	if err != nil {
		return nil, err
	}
	e.FinishedAt = e.FinishedAt.UTC()
	e.IngestedAt = e.IngestedAt.UTC()
	return &e, nil
}

func scanEvidenceRow(rows pgx.Rows) (*model.Evidence, error) {
	var e model.Evidence
	err := rows.Scan(
		&e.ID, &e.ClientRecordID, &e.Repo, &e.Branch, &e.RCSRef, &e.ProcedureRef,
		&e.EvidenceType, &e.Source, &e.Result, &e.FinishedAt, &e.IngestedAt, &e.Metadata,
	)
	if err != nil {
		return nil, err
	}
	e.FinishedAt = e.FinishedAt.UTC()
	e.IngestedAt = e.IngestedAt.UTC()
	return &e, nil
}

// distinctFields are the columns that may be queried via Distinct.
// Whitelisted to prevent SQL injection — the field name is interpolated directly.
var distinctFields = map[string]bool{
	"repo":          true,
	"evidence_type": true,
	"source":        true,
}

// Distinct returns up to `limit` distinct values for the given column,
// optionally filtered by a case-insensitive substring match against `query`.
// Returns an error if the field is not in the whitelist.
func (s *EvidenceStore) Distinct(ctx context.Context, field, query string, limit int) ([]string, error) {
	if !distinctFields[field] {
		return nil, fmt.Errorf("field %q is not queryable for distinct values", field)
	}
	if limit <= 0 {
		limit = 100
	}

	var (
		where string
		args  []any
	)
	if query != "" {
		where = fmt.Sprintf(" WHERE %s ILIKE $1", field)
		args = []any{"%" + query + "%"}
	}

	sql := fmt.Sprintf(
		"SELECT DISTINCT %s FROM evidence%s ORDER BY %s LIMIT %d",
		field, where, field, limit,
	)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query distinct %s: %w", field, err)
	}
	defer rows.Close()

	values := make([]string, 0)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan distinct row: %w", err)
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate distinct rows: %w", err)
	}
	return values, nil
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
			"SELECT "+evidenceColumns+" FROM evidence%s ORDER BY finished_at ASC, id ASC LIMIT %d",
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
