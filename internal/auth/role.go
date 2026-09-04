package auth

// Role is a named, fixed bundle of permissions. Roles are defined in code, not
// composed at runtime: the store has five of them against eleven permissions,
// and a role-CRUD API would be a larger surface than the thing it governs.
type Role string

const (
	RoleViewer      Role = "viewer"
	RoleContributor Role = "contributor"
	RoleCI          Role = "ci"
	RoleAdmin       Role = "admin"
	// RoleProvisioner is the directory's own credential. It provisions people
	// and does nothing else — no evidence, no analytics, not even a read.
	RoleProvisioner Role = "provisioner"
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
	// Nothing but provisioning, and deliberately not built on viewer: a
	// directory's token has no reason to read a test result, and this is the
	// one credential in the store that lives for years in somebody else's
	// configuration.
	provisionerPerms = newPermSet(
		PermSCIMProvision,
	)
	// admin holds provisioning too, so that an administrator can drive the SCIM
	// endpoints by hand — and so that a deployment can start provisioning
	// before it has minted a dedicated token.
	adminPerms = contributorPerms.with(
		PermInheritanceWrite,
		PermPrincipalAdmin,
		PermRetentionAdmin,
		PermSCIMProvision,
	)

	rolePermissions = map[Role]permSet{
		RoleViewer:      viewerPerms,
		RoleContributor: contributorPerms,
		RoleCI:          ciPerms,
		RoleAdmin:       adminPerms,
		RoleProvisioner: provisionerPerms,
	}
)

// RoleNames returns the role names in the order they widen, which is the order
// an administrator reads them in: a message listing what a caller could have
// sent, and the checkboxes in the web UI.
//
// provisioner comes last and outside that widening, because it is not a bigger
// version of anything above it — it is a different job.
func RoleNames() []string {
	return []string{
		string(RoleViewer),
		string(RoleContributor),
		string(RoleCI),
		string(RoleAdmin),
		string(RoleProvisioner),
	}
}

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
