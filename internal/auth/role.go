package auth

// Role is a named, fixed bundle of permissions. Roles are defined in code, not
// composed at runtime: the store has four of them against ten permissions, and
// a role-CRUD API would be a larger surface than the thing it governs.
type Role string

const (
	RoleViewer      Role = "viewer"
	RoleContributor Role = "contributor"
	RoleCI          Role = "ci"
	RoleAdmin       Role = "admin"
)

// permSet is a flattened set of permissions. Callers get one built once at
// authentication time rather than walking their roles on every check.
type permSet map[Permission]struct{}

func newPermSet(perms ...Permission) permSet {
	s := make(permSet, len(perms))
	for _, p := range perms {
		s[p] = struct{}{}
	}
	return s
}

// with returns a copy of s extended by perms, leaving s untouched — the role
// table below builds each role from the one before it.
func (s permSet) with(perms ...Permission) permSet {
	out := make(permSet, len(s)+len(perms))
	for p := range s {
		out[p] = struct{}{}
	}
	for _, p := range perms {
		out[p] = struct{}{}
	}
	return out
}

var (
	viewerPerms = newPermSet(
		PermEvidenceRead,
		PermAnalyticsRead,
		PermBlobRead,
		PermInheritanceRead,
	)
	contributorPerms = viewerPerms.with(
		PermEvidenceWrite,
		PermBlobWrite,
	)
	// A build robot legitimately writes a source that is not its own name —
	// the build URL — where a human should not. That is the only thing ci adds.
	ciPerms = contributorPerms.with(
		PermSourceAny,
	)
	// admin deliberately does not subsume ci. An administrator who wants to
	// backfill evidence under someone else's source should hold both roles
	// explicitly, so writing history in another party's name is always a
	// deliberate grant.
	adminPerms = contributorPerms.with(
		PermInheritanceWrite,
		PermPrincipalAdmin,
		PermRetentionAdmin,
	)

	rolePermissions = map[Role]permSet{
		RoleViewer:      viewerPerms,
		RoleContributor: contributorPerms,
		RoleCI:          ciPerms,
		RoleAdmin:       adminPerms,
	}
)

// ParseRole validates a role name coming from outside the process — config
// today, a role_bindings row tomorrow.
func ParseRole(s string) (Role, bool) {
	r := Role(s)
	if _, ok := rolePermissions[r]; ok {
		return r, true
	}
	return "", false
}

// Grants reports whether the role carries perm. Unknown roles grant
// nothing, so a binding that outlives the constant it names fails closed.
func (r Role) Grants(perm Permission) bool {
	_, ok := rolePermissions[r][perm]
	return ok
}
