package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/model"
)

// SCIMStore is the provisioning view of the principals table.
//
// Separate from PrincipalStore because the two answer different questions
// against the same rows: that one serves authentication and the Access tab,
// this one serves a directory that keeps its own idea of who exists and expects
// to address people by an id it was given.
//
// Nothing here deletes. A provisioner asking for a delete gets a disable, for
// the same reason the Access tab has no delete: evidence names its source, and
// a removed principal would leave records attributed to nothing.
type SCIMStore struct {
	pool *pgxpool.Pool
}

func NewSCIMStore(pool *pgxpool.Pool) *SCIMStore {
	return &SCIMStore{pool: pool}
}

// ErrUserNameTaken means the provisioner asked to create somebody who is
// already here under that login name.
var ErrUserNameTaken = errors.New("userName already belongs to another principal")

// ErrNotProvisionable means a principal answering to this name exists and is
// not a person — an API key called after one, say. Attaching a directory
// identity to it would hand a machine credential a human's account.
var ErrNotProvisionable = errors.New("an existing principal of another kind holds this name")

const scimColumns = `
	SELECT id, COALESCE(scim_id, ''), COALESCE(scim_external_id, ''),
	       COALESCE(user_name, ''), subject, display_name,
	       disabled_at IS NULL, created_at, updated_at
	FROM principals
`

func scanSCIMUser(row pgx.Row) (*model.SCIMUser, error) {
	var u model.SCIMUser
	err := row.Scan(&u.ID, &u.SCIMID, &u.ExternalID, &u.UserName,
		&u.Subject, &u.DisplayName, &u.Active, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read scim user: %w", err)
	}
	return &u, nil
}

// SCIMUserWrite is what a provisioner says about somebody. It is the same shape
// for a create and a replace, because SCIM's PUT is a whole-resource write.
type SCIMUserWrite struct {
	UserName    string
	ExternalID  string
	Subject     string
	DisplayName string
	Active      bool
}

// CreateUser provisions somebody, or adopts the principal they already are.
//
// Adoption is the mirror of the claiming rule in UpsertFromIdP, and exists for
// the same reason: a deployment that had single sign-on before it had
// provisioning is full of people who logged in and created their own principal.
// Refusing those would leave the directory permanently unable to manage anybody
// who got there first — and, worse, unable to deactivate them, which is the one
// thing provisioning is for.
//
// It is narrow in the same way. Only a person's row that no provisioner has
// claimed can be adopted; an API key that happens to be named after somebody
// stays a key.
func (s *SCIMStore) CreateUser(ctx context.Context, in SCIMUserWrite) (*model.SCIMUser, error) {
	scimID := uuid.NewString()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create scim user: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	// Anyone already answering to this name, however they got here.
	var existingID uuid.UUID
	var kind string
	var existingSCIM *string
	err = tx.QueryRow(ctx, `
		SELECT id, kind, scim_id FROM principals
		 WHERE subject = $1 OR (user_name IS NOT NULL AND user_name = $2)
		 LIMIT 1
	`, in.Subject, in.UserName).Scan(&existingID, &kind, &existingSCIM)
	switch {
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("look for an existing principal: %w", err)

	case err == nil && existingSCIM != nil:
		// Already provisioned. The directory should have found them with a
		// filter query first; saying so plainly is more use than a second row.
		return nil, fmt.Errorf("%w: %s", ErrUserNameTaken, in.UserName)

	case err == nil && kind != model.PrincipalKindUser:
		return nil, fmt.Errorf("%w: %s", ErrNotProvisionable, in.Subject)

	case err == nil:
		// A person who logged in before provisioning reached them. Attach the
		// directory's identity to the principal they already have, rather than
		// splitting them in two.
		if _, err := tx.Exec(ctx, `
			UPDATE principals
			   SET scim_id = $1, scim_external_id = NULLIF($2, ''), user_name = $3,
			       display_name = $4, disabled_at = $5, updated_at = now()
			 WHERE id = $6
		`, scimID, in.ExternalID, in.UserName, in.DisplayName, disabledAt(in.Active), existingID); err != nil {
			return nil, fmt.Errorf("adopt existing principal: %w", err)
		}

	default:
		if _, err := tx.Exec(ctx, `
			INSERT INTO principals (subject, kind, display_name, scim_id, scim_external_id, user_name, disabled_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7)
		`, in.Subject, model.PrincipalKindUser, in.DisplayName, scimID,
			in.ExternalID, in.UserName, disabledAt(in.Active)); err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: %s", ErrUserNameTaken, in.UserName)
			}
			return nil, fmt.Errorf("create scim user: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create scim user: %w", err)
	}
	return s.FindUser(ctx, scimID)
}

// FindUser resolves the id this store handed the provisioner. A miss is (nil,
// nil): the provisioner asking about somebody who is not here is an answer, not
// a failure.
func (s *SCIMStore) FindUser(ctx context.Context, scimID string) (*model.SCIMUser, error) {
	return scanSCIMUser(s.pool.QueryRow(ctx, scimColumns+` WHERE scim_id = $1`, scimID))
}

// SCIMUserFilter is the slice of a listing a provisioner asked for. The two
// equality filters are the only ones Entra sends, and the only ones supported;
// see docs/scim-provisioning-plan.md.
type SCIMUserFilter struct {
	UserName   string
	ExternalID string
	// StartIndex is SCIM's, so 1-based. Count is the page size.
	StartIndex int
	Count      int
}

// ListUsers returns one page and the total that matched, which is what a
// provisioner needs to page through a directory.
func (s *SCIMStore) ListUsers(ctx context.Context, f SCIMUserFilter) ([]model.SCIMUser, int, error) {
	where := []string{"scim_id IS NOT NULL"}
	args := []any{}
	if f.UserName != "" {
		args = append(args, f.UserName)
		where = append(where, fmt.Sprintf("user_name = $%d", len(args)))
	}
	if f.ExternalID != "" {
		args = append(args, f.ExternalID)
		where = append(where, fmt.Sprintf("scim_external_id = $%d", len(args)))
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM principals`+clause, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count scim users: %w", err)
	}

	// Ordered by creation so that paging through a directory sees every row
	// once. Ordering by name would move rows between pages as they are renamed
	// mid-sync.
	args = append(args, f.Count, f.StartIndex-1)
	rows, err := s.pool.Query(ctx, scimColumns+clause+
		fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list scim users: %w", err)
	}
	defer rows.Close()

	users := make([]model.SCIMUser, 0)
	for rows.Next() {
		u, err := scanSCIMUser(rows)
		if err != nil {
			return nil, 0, err
		}
		users = append(users, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read scim users: %w", err)
	}
	return users, total, nil
}

// ReplaceUser is SCIM's PUT: the provisioner states the whole resource.
//
// The subject is left alone. It is what evidence already filed names its author
// by, and a directory changing somebody's login name is not a reason to rewrite
// the attribution on records they wrote last year.
func (s *SCIMStore) ReplaceUser(ctx context.Context, scimID string, in SCIMUserWrite) (*model.SCIMUser, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE principals
		   SET user_name = $1, scim_external_id = NULLIF($2, ''), display_name = $3,
		       disabled_at = $4, updated_at = now()
		 WHERE scim_id = $5
	`, in.UserName, in.ExternalID, in.DisplayName, disabledAt(in.Active), scimID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrUserNameTaken, in.UserName)
		}
		return nil, fmt.Errorf("replace scim user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	return s.FindUser(ctx, scimID)
}

// SetActive is the deprovisioning path, and the reason this protocol is worth
// speaking at all.
//
// Deactivating ends their sessions in the same transaction as it disables the
// account. Disabling alone would leave the browser they walked away from
// working until it expired on its own, which is the failure provisioning is
// meant to close — an account is not shut while a live session still holds it.
func (s *SCIMStore) SetActive(ctx context.Context, scimID string, active bool) (*model.SCIMUser, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin set active: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE principals SET disabled_at = $1, updated_at = now()
		 WHERE scim_id = $2
		 RETURNING id
	`, disabledAt(active), scimID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("set scim user active: %w", err)
	}

	if !active {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE principal_id = $1`, id); err != nil {
			return nil, fmt.Errorf("end sessions of deactivated user: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit set active: %w", err)
	}
	return s.FindUser(ctx, scimID)
}

// CountOtherEnabledAdmins reports how many principals besides this one can
// still administer the store, so that a provisioner cannot deactivate the last
// way in. The Access tab makes the same check for the same reason.
func (s *SCIMStore) CountOtherEnabledAdmins(ctx context.Context, scimID string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*)
		  FROM principals p
		  JOIN role_bindings rb ON rb.principal_id = p.id AND rb.scope = $1
		 WHERE rb.role = 'admin'
		   AND p.disabled_at IS NULL
		   AND (p.scim_id IS DISTINCT FROM $2)
	`, model.ScopeStoreWide, scimID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count other admins: %w", err)
	}
	return n, nil
}

// HasAdminRole reports whether this principal can administer the store, by any
// grant — a role the directory implied or one an administrator gave by hand.
func (s *SCIMStore) HasAdminRole(ctx context.Context, principalID uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM role_bindings
		     WHERE principal_id = $1 AND scope = $2 AND role = 'admin'
		)
	`, principalID, model.ScopeStoreWide).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("read admin role: %w", err)
	}
	return exists, nil
}

// disabledAt turns SCIM's active flag into the timestamp this store revokes
// with — a timestamp rather than a boolean because "when were they shut out" is
// a question an auditor asks and a boolean cannot answer.
//
// Note this re-stamps the moment on every write that says inactive. For the
// only caller that matters — the directory reporting a departure — that is the
// moment it told us, which is the truthful answer.
func disabledAt(active bool) *time.Time {
	if active {
		return nil
	}
	now := time.Now().UTC()
	return &now
}

func isUniqueViolation(err error) bool {
	return strings.Contains(err.Error(), "duplicate key value")
}
