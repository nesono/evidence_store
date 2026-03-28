package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iss/evidence-store/internal/model"
)

type InheritanceStore struct {
	pool *pgxpool.Pool
}

func NewInheritanceStore(pool *pgxpool.Pool) *InheritanceStore {
	return &InheritanceStore{pool: pool}
}

func (s *InheritanceStore) Insert(ctx context.Context, c *model.InheritanceCreate) (*model.InheritanceDeclaration, error) {
	scope := c.Scope
	if scope == nil {
		scope = json.RawMessage(`[]`)
	}

	var d model.InheritanceDeclaration
	err := s.pool.QueryRow(ctx, `
		INSERT INTO inheritance_declaration (repo, source_rcs_ref, target_rcs_ref, scope, justification, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at, repo, source_rcs_ref, target_rcs_ref, scope, justification, created_by
	`, c.Repo, c.SourceRCSRef, c.TargetRCSRef, scope, c.Justification, c.CreatedBy).Scan(
		&d.ID, &d.CreatedAt, &d.Repo, &d.SourceRCSRef, &d.TargetRCSRef, &d.Scope, &d.Justification, &d.CreatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("insert inheritance: %w", err)
	}

	return &d, nil
}

func (s *InheritanceStore) List(ctx context.Context, filter model.InheritanceFilter) ([]model.InheritanceDeclaration, error) {
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

	if v := filter.Repo; v != nil {
		where = append(where, fmt.Sprintf("repo = %s", arg(*v)))
	}
	if v := filter.SourceRCSRef; v != nil {
		where = append(where, fmt.Sprintf("source_rcs_ref = %s", arg(*v)))
	}
	if v := filter.TargetRCSRef; v != nil {
		where = append(where, fmt.Sprintf("target_rcs_ref = %s", arg(*v)))
	}

	query := "SELECT id, created_at, repo, source_rcs_ref, target_rcs_ref, scope, justification, created_by FROM inheritance_declaration"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query inheritance: %w", err)
	}
	defer rows.Close()

	var results []model.InheritanceDeclaration
	for rows.Next() {
		var d model.InheritanceDeclaration
		if err := rows.Scan(
			&d.ID, &d.CreatedAt, &d.Repo, &d.SourceRCSRef, &d.TargetRCSRef, &d.Scope, &d.Justification, &d.CreatedBy,
		); err != nil {
			return nil, fmt.Errorf("scan inheritance: %w", err)
		}
		results = append(results, d)
	}

	return results, rows.Err()
}

// FindForTarget returns all inheritance declarations for a given repo + target rcs_ref.
func (s *InheritanceStore) FindForTarget(ctx context.Context, repo, targetRCSRef string) ([]model.InheritanceDeclaration, error) {
	return s.List(ctx, model.InheritanceFilter{
		Repo:         &repo,
		TargetRCSRef: &targetRCSRef,
	})
}
