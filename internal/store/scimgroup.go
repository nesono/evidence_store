package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/nesono/evidence-store/internal/model"
)

// RolesForGroups maps the groups somebody is in to the roles they should hold.
//
// Passed in as a function rather than read here, because deciding which of our
// roles a group name means — and dropping a role this binary no longer defines
// — belongs to internal/auth, and this package is below it. It also keeps the
// whole reconciliation inside one transaction, which matters: a membership
// change that wrote the groups but not the roles would leave somebody holding
// access the directory has just taken away.
type RolesForGroups func(groups []string) []string

// ErrGroupNameTaken means a provisioner asked to create a group that is here.
var ErrGroupNameTaken = errors.New("displayName already belongs to another group")

const scimGroupColumns = `
	SELECT id, scim_id, COALESCE(external_id, ''), display_name, created_at, updated_at
	FROM scim_groups
`

func scanSCIMGroup(row pgx.Row) (*model.SCIMGroup, error) {
	var g model.SCIMGroup
	err := row.Scan(&g.ID, &g.SCIMID, &g.ExternalID, &g.DisplayName, &g.CreatedAt, &g.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read scim group: %w", err)
	}
	return &g, nil
}

// SCIMGroupWrite is a group as a provisioner states it. Members are the SCIM
// ids of users, which is how the protocol refers to them.
type SCIMGroupWrite struct {
	DisplayName string
	ExternalID  string
	Members     []string
}

// CreateGroup records a group and grants what its membership implies.
func (s *SCIMStore) CreateGroup(ctx context.Context, in SCIMGroupWrite, rolesFor RolesForGroups) (*model.SCIMGroup, error) {
	scimID := uuid.NewString()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create group: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var groupID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO scim_groups (scim_id, external_id, display_name)
		VALUES ($1, NULLIF($2, ''), $3)
		RETURNING id
	`, scimID, in.ExternalID, in.DisplayName).Scan(&groupID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrGroupNameTaken, in.DisplayName)
		}
		return nil, fmt.Errorf("create scim group: %w", err)
	}

	affected, err := setMembers(ctx, tx, groupID, in.Members)
	if err != nil {
		return nil, err
	}
	if err := reconcileSCIMRoles(ctx, tx, affected, rolesFor); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create group: %w", err)
	}
	return s.FindGroup(ctx, scimID)
}

func (s *SCIMStore) FindGroup(ctx context.Context, scimID string) (*model.SCIMGroup, error) {
	group, err := scanSCIMGroup(s.pool.QueryRow(ctx, scimGroupColumns+` WHERE scim_id = $1`, scimID))
	if err != nil || group == nil {
		return nil, err
	}
	group.Members, err = s.membersOf(ctx, group.ID)
	return group, err
}

// membersOf lists a group's members as the provisioner refers to them. A member
// this store cannot name in SCIM terms is left out rather than reported with an
// id the provisioner has never seen.
func (s *SCIMStore) membersOf(ctx context.Context, groupID uuid.UUID) ([]model.SCIMGroupMember, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.scim_id, COALESCE(p.user_name, p.subject)
		  FROM scim_group_members m
		  JOIN principals p ON p.id = m.principal_id
		 WHERE m.group_id = $1 AND p.scim_id IS NOT NULL
		 ORDER BY p.subject
	`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list group members: %w", err)
	}
	defer rows.Close()

	members := make([]model.SCIMGroupMember, 0)
	for rows.Next() {
		var m model.SCIMGroupMember
		if err := rows.Scan(&m.SCIMID, &m.Display); err != nil {
			return nil, fmt.Errorf("read group member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// SCIMGroupFilter is the one equality filter a provisioner sends for groups.
type SCIMGroupFilter struct {
	DisplayName string
	StartIndex  int
	Count       int
}

func (s *SCIMStore) ListGroups(ctx context.Context, f SCIMGroupFilter) ([]model.SCIMGroup, int, error) {
	where, args := "", []any{}
	if f.DisplayName != "" {
		args = append(args, f.DisplayName)
		where = " WHERE display_name = $1"
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM scim_groups`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count scim groups: %w", err)
	}

	args = append(args, f.Count, f.StartIndex-1)
	rows, err := s.pool.Query(ctx, scimGroupColumns+where+
		fmt.Sprintf(" ORDER BY created_at, id LIMIT $%d OFFSET $%d", len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list scim groups: %w", err)
	}
	defer rows.Close()

	groups := make([]model.SCIMGroup, 0)
	for rows.Next() {
		g, err := scanSCIMGroup(rows)
		if err != nil {
			return nil, 0, err
		}
		groups = append(groups, *g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read scim groups: %w", err)
	}
	for i := range groups {
		if groups[i].Members, err = s.membersOf(ctx, groups[i].ID); err != nil {
			return nil, 0, err
		}
	}
	return groups, total, nil
}

// ReplaceGroup is PUT: the provisioner states the whole group, membership
// included. Anyone dropped from it loses what it granted them.
func (s *SCIMStore) ReplaceGroup(ctx context.Context, scimID string, in SCIMGroupWrite, rolesFor RolesForGroups) (*model.SCIMGroup, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin replace group: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var groupID uuid.UUID
	err = tx.QueryRow(ctx, `
		UPDATE scim_groups
		   SET display_name = $1, external_id = NULLIF($2, ''), updated_at = now()
		 WHERE scim_id = $3
		 RETURNING id
	`, in.DisplayName, in.ExternalID, scimID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("%w: %s", ErrGroupNameTaken, in.DisplayName)
		}
		return nil, fmt.Errorf("replace scim group: %w", err)
	}

	// Everyone in the group before and after: a rename changes what the group
	// grants, and the people leaving need their roles recomputed as much as the
	// people arriving.
	before, err := memberIDs(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scim_group_members WHERE group_id = $1`, groupID); err != nil {
		return nil, fmt.Errorf("clear group members: %w", err)
	}
	after, err := setMembers(ctx, tx, groupID, in.Members)
	if err != nil {
		return nil, err
	}
	if err := reconcileSCIMRoles(ctx, tx, union(before, after), rolesFor); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit replace group: %w", err)
	}
	return s.FindGroup(ctx, scimID)
}

// PatchGroupMembers adds and removes members, which is how a directory reports
// somebody joining or leaving a team.
//
// Removal is the one that matters: it is how a role is taken away from
// somebody who keeps their account. Losing it would leave a promotion
// reversible only by deleting the person.
func (s *SCIMStore) PatchGroupMembers(ctx context.Context, scimID string, add, remove []string, displayName string, rolesFor RolesForGroups) (*model.SCIMGroup, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin patch group: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var groupID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM scim_groups WHERE scim_id = $1`, scimID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find scim group: %w", err)
	}

	if displayName != "" {
		if _, err := tx.Exec(ctx,
			`UPDATE scim_groups SET display_name = $1, updated_at = now() WHERE id = $2`,
			displayName, groupID); err != nil {
			if isUniqueViolation(err) {
				return nil, fmt.Errorf("%w: %s", ErrGroupNameTaken, displayName)
			}
			return nil, fmt.Errorf("rename scim group: %w", err)
		}
	}

	affected, err := setMembers(ctx, tx, groupID, add)
	if err != nil {
		return nil, err
	}

	removed, err := principalIDsOfSCIMIDs(ctx, tx, remove)
	if err != nil {
		return nil, err
	}
	if len(removed) > 0 {
		if _, err := tx.Exec(ctx,
			`DELETE FROM scim_group_members WHERE group_id = $1 AND principal_id = ANY($2)`,
			groupID, removed); err != nil {
			return nil, fmt.Errorf("remove group members: %w", err)
		}
	}

	// A rename changes what the group grants, so everybody in it is affected,
	// not only those named in this patch.
	current, err := memberIDs(ctx, tx, groupID)
	if err != nil {
		return nil, err
	}
	if err := reconcileSCIMRoles(ctx, tx, union(union(affected, removed), current), rolesFor); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit patch group: %w", err)
	}
	return s.FindGroup(ctx, scimID)
}

// DeleteGroup removes the group and the access it granted.
//
// A real delete, unlike a user: a group holds no evidence and names nothing a
// reader will look up later, so keeping a husk of one would only leave a name
// the role map could still match.
func (s *SCIMStore) DeleteGroup(ctx context.Context, scimID string, rolesFor RolesForGroups) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin delete group: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed

	var groupID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM scim_groups WHERE scim_id = $1`, scimID).Scan(&groupID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find scim group: %w", err)
	}

	members, err := memberIDs(ctx, tx, groupID)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM scim_groups WHERE id = $1`, groupID); err != nil {
		return false, fmt.Errorf("delete scim group: %w", err)
	}
	// After the delete, so the recomputation sees the group gone and takes away
	// what it was granting.
	if err := reconcileSCIMRoles(ctx, tx, members, rolesFor); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit delete group: %w", err)
	}
	return true, nil
}

// --- Reconciliation ---

// reconcileSCIMRoles makes each person's directory-granted roles match the
// groups they are now in, and touches nothing else.
//
// Only the 'scim' bindings are rewritten. A role an administrator granted by
// hand survives a sync — the same promise the login path makes — and so do the
// roles a login derived from its own token, which are reconciled on their own
// schedule against a source that may legitimately disagree with this one.
func reconcileSCIMRoles(ctx context.Context, tx pgx.Tx, principalIDs []uuid.UUID, rolesFor RolesForGroups) error {
	for _, id := range principalIDs {
		groups, err := groupNamesOf(ctx, tx, id)
		if err != nil {
			return err
		}
		roles := rolesFor(groups)

		if _, err := tx.Exec(ctx, `
			DELETE FROM role_bindings
			 WHERE principal_id = $1 AND scope = $2 AND source = $3
			   AND role <> ALL($4::text[])
		`, id, model.ScopeStoreWide, model.GrantSourceSCIM, roles); err != nil {
			return fmt.Errorf("remove stale scim roles: %w", err)
		}
		for _, role := range roles {
			if _, err := tx.Exec(ctx, `
				INSERT INTO role_bindings (principal_id, role, scope, source)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (principal_id, role, scope) DO NOTHING
			`, id, role, model.ScopeStoreWide, model.GrantSourceSCIM); err != nil {
				return fmt.Errorf("grant scim role %q: %w", role, err)
			}
		}
	}
	return nil
}

func groupNamesOf(ctx context.Context, tx pgx.Tx, principalID uuid.UUID) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT g.display_name
		  FROM scim_group_members m
		  JOIN scim_groups g ON g.id = m.group_id
		 WHERE m.principal_id = $1
	`, principalID)
	if err != nil {
		return nil, fmt.Errorf("read group membership: %w", err)
	}
	defer rows.Close()

	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read group name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// setMembers adds the named users to a group and returns who was affected.
//
// A member the store has never heard of is skipped rather than refused: a
// directory syncing a group whose members it has not provisioned yet is
// ordinary, and failing the whole request would stall the sync on its first
// group.
func setMembers(ctx context.Context, tx pgx.Tx, groupID uuid.UUID, scimIDs []string) ([]uuid.UUID, error) {
	ids, err := principalIDsOfSCIMIDs(ctx, tx, scimIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO scim_group_members (group_id, principal_id)
			VALUES ($1, $2) ON CONFLICT DO NOTHING
		`, groupID, id); err != nil {
			return nil, fmt.Errorf("add group member: %w", err)
		}
	}
	return ids, nil
}

func principalIDsOfSCIMIDs(ctx context.Context, tx pgx.Tx, scimIDs []string) ([]uuid.UUID, error) {
	if len(scimIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id FROM principals WHERE scim_id = ANY($1)`, scimIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve group members: %w", err)
	}
	defer rows.Close()

	ids := make([]uuid.UUID, 0, len(scimIDs))
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read member id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func memberIDs(ctx context.Context, tx pgx.Tx, groupID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx,
		`SELECT principal_id FROM scim_group_members WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("read group members: %w", err)
	}
	defer rows.Close()

	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read member id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// union merges two sets of principals, so a reconciliation runs once per person
// however many ways this request touched them.
func union(a, b []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(a)+len(b))
	out := make([]uuid.UUID, 0, len(a)+len(b))
	for _, id := range append(append([]uuid.UUID{}, a...), b...) {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
