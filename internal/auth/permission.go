package auth

// Permission is a single capability a caller may hold. The set is closed:
// every constant below is declared here and nowhere else, so the answer to
// "what can a caller do in this store?" is this file rather than a survey of
// route definitions.
type Permission string

const (
	PermEvidenceRead     Permission = "evidence:read"
	PermEvidenceWrite    Permission = "evidence:write"
	PermAnalyticsRead    Permission = "analytics:read"
	PermBlobRead         Permission = "blob:read"
	PermBlobWrite        Permission = "blob:write"
	PermInheritanceRead  Permission = "inheritance:read"
	PermInheritanceWrite Permission = "inheritance:write"

	// PermSourceAny allows writing evidence whose source is not the caller's
	// own subject. Enforced in the evidence handler rather than in middleware,
	// since only the handler has parsed the body. Not wired up yet — phase 3.
	PermSourceAny Permission = "source:any"

	// PermPrincipalAdmin and PermRetentionAdmin guard surfaces that do not
	// exist yet (principal CRUD in phase 4; retention is a background worker
	// with no HTTP endpoint at all). They are declared now so the role table
	// below is the whole story rather than a partial one.
	PermPrincipalAdmin Permission = "principal:admin"
	PermRetentionAdmin Permission = "retention:admin"

	// PermSCIMProvision allows a directory to create, update and deactivate
	// people through SCIM.
	//
	// Its own permission rather than principal:admin, which provisioning used
	// while it was being built. A token that sits in another company's
	// configuration for years should be able to do one thing: it has no reason
	// to read a single test result, and every reason not to be able to.
	PermSCIMProvision Permission = "scim:provision"
)
