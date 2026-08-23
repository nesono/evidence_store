package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/model"
)

// PrincipalStore reads and writes the identities behind API keys.
//
// It answers who a credential belongs to and nothing about what that identity
// may do: roles come back as the strings the database holds, and internal/auth
// decides what they grant. Keeping the decision there is what lets a binding
// naming a role this binary has never heard of fail closed instead of erroring
// out a request.
type PrincipalStore struct {
	pool *pgxpool.Pool
}

func NewPrincipalStore(pool *pgxpool.Pool) *PrincipalStore {
	return &PrincipalStore{pool: pool}
}

// principalColumns is shared by the two lookups so they cannot drift apart.
//
// The join asserts scope = '*'. Per-repo scoping is reserved and inert
// (migration 000006), and reading a scoped binding as though it were store-wide
// is exactly the silent widening the column's default exists to prevent.
const principalColumns = `
	SELECT p.id, p.subject, p.kind, p.display_name,
	       p.disabled_at, p.created_at, p.last_seen_at,
	       COALESCE(
	           ARRAY_AGG(rb.role ORDER BY rb.role) FILTER (WHERE rb.role IS NOT NULL),
	           '{}'
	       ) AS roles
	FROM principals p
	LEFT JOIN role_bindings rb
	       ON rb.principal_id = p.id AND rb.scope = '*'
`

// FindByKeyHash resolves a hashed bearer token to its principal. This is the
// authentication hot path: one indexed equality check plus the roles.
//
// A token that matches nothing returns (nil, nil). That is an answer, not a
// failure — most callers presenting an unknown key are simply not ours — and it
// keeps a database outage, which does return an error, distinguishable from a
// wrong key.
func (s *PrincipalStore) FindByKeyHash(ctx context.Context, keyHash string) (*model.Principal, error) {
	return s.queryOne(ctx, principalColumns+` WHERE p.key_hash = $1 GROUP BY p.id`, keyHash)
}

// FindBySubject resolves a principal by name. Used by bootstrapping and by the
// principal admin API.
func (s *PrincipalStore) FindBySubject(ctx context.Context, subject string) (*model.Principal, error) {
	return s.queryOne(ctx, principalColumns+` WHERE p.subject = $1 GROUP BY p.id`, subject)
}

// FindByID resolves a principal by its row identity, which is what the admin
// API addresses them by — a subject can be long and awkward in a URL, and an ID
// is what a UI already holds after listing.
func (s *PrincipalStore) FindByID(ctx context.Context, id uuid.UUID) (*model.Principal, error) {
	return s.queryOne(ctx, principalColumns+` WHERE p.id = $1 GROUP BY p.id`, id)
}

// List returns every principal, disabled ones included. Revoked credentials are
// exactly what an administrator reviewing access needs to see, so filtering
// them out here would hide the answer to the question being asked.
//
// Unpaginated: this table holds an organisation's keys and people, not its
// evidence, and a deployment with enough of them to need paging has a bigger
// problem than a long page.
func (s *PrincipalStore) List(ctx context.Context) ([]model.Principal, error) {
	rows, err := s.pool.Query(ctx, principalColumns+` GROUP BY p.id ORDER BY p.subject`)
	if err != nil {
		return nil, fmt.Errorf("list principals: %w", err)
	}
	defer rows.Close()

	principals := make([]model.Principal, 0)
	for rows.Next() {
		var p model.Principal
		if err := rows.Scan(
			&p.ID, &p.Subject, &p.Kind, &p.DisplayName,
			&p.DisabledAt, &p.CreatedAt, &p.LastSeenAt, &p.Roles,
		); err != nil {
			return nil, fmt.Errorf("scan principal: %w", err)
		}
		principals = append(principals, p)
	}
	return principals, rows.Err()
}

// SetDisabled revokes a principal or restores it. Revocation is a timestamp
// rather than a DELETE so that evidence already attributed to the principal
// still names something, and so an administrator can see that a credential was
// taken away rather than never having existed.
//
// Setting the state it is already in is not an error, but re-disabling an
// already-disabled principal leaves the original timestamp alone: when access
// was withdrawn is the fact worth keeping.
func (s *PrincipalStore) SetDisabled(ctx context.Context, id uuid.UUID, disabled bool) (*model.Principal, error) {
	var query string
	if disabled {
		query = `UPDATE principals SET disabled_at = now() WHERE id = $1 AND disabled_at IS NULL`
	} else {
		query = `UPDATE principals SET disabled_at = NULL WHERE id = $1`
	}
	if _, err := s.pool.Exec(ctx, query, id); err != nil {
		return nil, fmt.Errorf("set principal disabled=%t: %w", disabled, err)
	}
	return s.FindByID(ctx, id)
}

// ReplaceRoles makes the principal's store-wide grants exactly roles.
//
// Replacing rather than granting and revoking one at a time is what an
// administrator editing a set of checkboxes actually means, and it is
// idempotent: two admins submitting the same intent do not fight. It is one
// transaction, so a principal is never briefly left holding neither the old set
// nor the new one.
//
// Scoped bindings are left untouched. They grant nothing today and are not
// this API's to guess about.
func (s *PrincipalStore) ReplaceRoles(ctx context.Context, id uuid.UUID, roles []string, grantedBy *uuid.UUID) (*model.Principal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin replace roles: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	if _, err := tx.Exec(ctx,
		`DELETE FROM role_bindings WHERE principal_id = $1 AND scope = $2`,
		id, model.ScopeStoreWide,
	); err != nil {
		return nil, fmt.Errorf("clear roles: %w", err)
	}

	for _, role := range roles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_bindings (principal_id, role, scope, granted_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (principal_id, role, scope) DO NOTHING
		`, id, role, model.ScopeStoreWide, grantedBy); err != nil {
			return nil, fmt.Errorf("grant role %q: %w", role, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit replace roles: %w", err)
	}
	return s.FindByID(ctx, id)
}

// RotateKey replaces a principal's credential, keeping the identity. The old
// key stops working immediately, which is the point: a leaked key is fixed
// without orphaning the evidence already filed under the name that leaked it.
func (s *PrincipalStore) RotateKey(ctx context.Context, id uuid.UUID, keyHash string) (*model.Principal, error) {
	// The kind guard is what stops this quietly turning a human principal into
	// a bearer token, which the table's own CHECK would otherwise refuse in a
	// less legible way.
	tag, err := s.pool.Exec(ctx,
		`UPDATE principals SET key_hash = $2 WHERE id = $1 AND kind = $3`,
		id, keyHash, model.PrincipalKindAPIKey)
	if err != nil {
		return nil, fmt.Errorf("rotate principal key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	return s.FindByID(ctx, id)
}

// CountOtherEnabledAdmins reports how many principals besides this one can
// still administer the store. It backs the guard on the two operations that
// could otherwise leave a deployment with no way in but psql: disabling the
// last administrator, and taking the admin role off them.
func (s *PrincipalStore) CountOtherEnabledAdmins(ctx context.Context, excluding uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT p.id)
		FROM principals p
		JOIN role_bindings rb ON rb.principal_id = p.id
		WHERE p.id <> $1
		  AND p.disabled_at IS NULL
		  AND rb.role = 'admin'
		  AND rb.scope = $2
	`, excluding, model.ScopeStoreWide).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count other enabled admins: %w", err)
	}
	return n, nil
}

func (s *PrincipalStore) queryOne(ctx context.Context, query string, arg any) (*model.Principal, error) {
	var p model.Principal
	err := s.pool.QueryRow(ctx, query, arg).Scan(
		&p.ID, &p.Subject, &p.Kind, &p.DisplayName,
		&p.DisabledAt, &p.CreatedAt, &p.LastSeenAt, &p.Roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query principal: %w", err)
	}
	return &p, nil
}

// Insert creates a principal and its role bindings, or reports that the
// subject is already taken.
//
// Identity and roles go in one transaction. Split across two statements, a
// failure between them would leave a credential that has been handed to
// somebody and can do nothing — and by then the plaintext key exists nowhere
// to hand out again.
//
// A taken subject returns (nil, nil) rather than an error: two replicas
// starting at once both try to seed the bootstrap admin, and the one that loses
// has nothing to report — the identity it wanted exists.
func (s *PrincipalStore) Insert(ctx context.Context, in model.PrincipalCreate) (*model.Principal, error) {
	var keyHash *string
	if in.KeyHash != "" {
		keyHash = &in.KeyHash
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin insert principal: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var p model.Principal
	err = tx.QueryRow(ctx, `
		INSERT INTO principals (subject, kind, display_name, key_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (subject) DO NOTHING
		RETURNING id, subject, kind, display_name, disabled_at, created_at, last_seen_at
	`, in.Subject, in.Kind, in.DisplayName, keyHash).Scan(
		&p.ID, &p.Subject, &p.Kind, &p.DisplayName,
		&p.DisabledAt, &p.CreatedAt, &p.LastSeenAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("insert principal: %w", err)
	}

	p.Roles = make([]string, 0, len(in.Roles))
	for _, role := range in.Roles {
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_bindings (principal_id, role, scope, granted_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (principal_id, role, scope) DO NOTHING
		`, p.ID, role, model.ScopeStoreWide, in.GrantedBy); err != nil {
			return nil, fmt.Errorf("grant role %q: %w", role, err)
		}
		p.Roles = append(p.Roles, role)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit insert principal: %w", err)
	}
	return &p, nil
}

// touchInterval is how stale last_seen_at is allowed to get. The column exists
// to answer "is this key still in use", which a minute's resolution answers as
// well as a millisecond's — and a write per request on a store sized for CI
// traffic would cost far more than the answer is worth.
const touchInterval = "1 minute"

// TouchLastSeen records that a principal authenticated. The predicate does the
// throttling in the database, so replicas do not each need their own clock.
func (s *PrincipalStore) TouchLastSeen(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE principals
		SET last_seen_at = now()
		WHERE id = $1
		  AND (last_seen_at IS NULL OR last_seen_at < now() - INTERVAL '`+touchInterval+`')
	`, id)
	if err != nil {
		return fmt.Errorf("touch principal last_seen_at: %w", err)
	}
	return nil
}
