package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nesono/evidence-store/internal/blob"
)

// APIKey represents a configured API key with its access role.
type APIKey struct {
	Key      string
	ReadOnly bool
}

type Config struct {
	DatabaseURL     string
	ListenAddr      string
	DefaultPageSize int
	MaxPageSize     int
	MaxBatchSize    int
	LogLevel        string
	APIKeys         []APIKey
	Auth            Auth
	RateLimit       RateLimit
	// AnalyticsCacheTTL is how long an aggregation is reused for an identical
	// filter. Zero disables caching. Sorting and paging are applied after the
	// query, so this mostly serves those without a round trip; the cost is that
	// a window can lag new evidence by up to this long.
	AnalyticsCacheTTL time.Duration
	// QueryTimeout bounds how long a single evidence query may run — a search
	// as well as an aggregation. Zero means no budget beyond the server's own
	// request timeout. One value rather than two because a caller who has hit
	// the limit on one endpoint should meet the same limit, and the same
	// wording, on the other.
	QueryTimeout time.Duration
	Blob         Blob
	Weather      Weather
}

// Auth configures database-backed identities — principals with names, roles
// granted one at a time, and revocation that takes effect on the next request.
//
// It is off by default, and off is not the same as open: with DB set the store
// still honours APIKeys, and with neither the API is unauthenticated as it has
// always been.
type Auth struct {
	// DB turns on the principals table as a source of credentials. It never
	// falls open: once set, an empty table means nobody may in.
	DB bool
	// BootstrapAdmin is the subject of an administrator seeded on first start,
	// whose one-time key is logged. Without it a fresh database has no way in,
	// since issuing the first key is itself an administrator's operation.
	BootstrapAdmin string
	// OIDC lets people log in with the identity provider they already use.
	OIDC OIDC
	// SAML is the same thing for a directory that speaks SAML instead. Both can
	// be configured at once; a company migrating between them will have a
	// period where both are true.
	SAML SAML
	// RoleMap turns a group an identity provider reports into a role here, for
	// whichever provider reported it. A group with no entry grants nothing, so
	// adding the store to a company directory does not hand every employee an
	// account that can write.
	//
	// Shared rather than per-provider because it is one question — which of our
	// groups means which of your roles — and an operator running both front
	// ends against one directory should answer it once.
	RoleMap map[string]string
	// Session is what any login leaves behind, whichever front end made it.
	Session Session
}

// SAML configures single sign-on against a directory that speaks SAML. Empty
// IDPMetadataURL and IDPMetadataFile switch it off, which is the default.
//
// Everything behind Principal is shared with OIDC; this is a second front end,
// which is the arrangement docs/rbac-design.md was written to make possible.
type SAML struct {
	// Exactly one of these says where to find the provider's metadata: a URL to
	// fetch at startup, or a file for a deployment that will not reach out.
	IDPMetadataURL  string
	IDPMetadataFile string
	// RootURL is this store's own public address, e.g.
	// https://evidence.example.com. Every URL in the metadata handed to the
	// provider is built from it, so a wrong value produces metadata the
	// provider will accept and a login it will misdeliver.
	RootURL string
	// EntityID names this service provider to the identity provider. Defaults
	// to the metadata URL, which is the usual convention.
	EntityID string
	// CertFile and KeyFile are the service provider's X.509 keypair, used to
	// sign authentication requests and decrypt assertions. Required: most
	// providers will not register a service provider without one, and a
	// self-signed pair is a single openssl command.
	CertFile string
	KeyFile  string
	// Which assertion attributes carry what. Providers disagree wildly, and
	// the long URN forms are what Entra and ADFS actually send.
	EmailAttribute  string
	NameAttribute   string
	GroupsAttribute string
}

// Enabled reports whether a SAML login flow should be mounted at all.
func (s SAML) Enabled() bool { return s.IDPMetadataURL != "" || s.IDPMetadataFile != "" }

// OIDC configures single sign-on. Empty Issuer switches it off, which is the
// default: a store nobody has pointed at an identity provider has no login
// flow, and its API keys work exactly as before.
type OIDC struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURL is where the provider sends the browser back, and must match
	// what is registered with them. It is spelled out rather than derived from
	// the request because behind a proxy the request's own host is whatever the
	// proxy chose to forward, and guessing it wrong sends people to a URL the
	// provider will refuse.
	RedirectURL string
	Scopes      []string
	// GroupsClaim is the token claim carrying group membership. Providers
	// disagree: "groups" is common, Entra says "roles".
	GroupsClaim string
	// RoleMap turns a group the provider reports into a role here. A group with
	// no entry grants nothing, so adding the store to an IdP does not hand
	// every employee an account that can write.
	//
	// A copy of Auth.RoleMap, which is where the setting lives now that two
	// front ends share it.
	RoleMap map[string]string
}

// Session configures what a login leaves behind, for whichever front end made
// it. A session is a session regardless of how somebody proved who they were.
type Session struct {
	// TTL is how long a login lasts before the person signs in again.
	TTL time.Duration
	// CookieSecure keeps the session cookie off plain HTTP. On by default and
	// only worth turning off for local development, which is the one place a
	// store is legitimately reached without TLS.
	CookieSecure bool
}

// Enabled reports whether a login flow should be mounted at all.
func (o OIDC) Enabled() bool { return o.Issuer != "" }

// Weather configures the one lookup this store makes to a service outside it.
//
// It is a setting and not a constant because the deployments that most need
// weather on a test record are the ones on a proving ground behind a firewall:
// they need to point this at a mirror, at their own site's instruments, or at
// nothing at all.
type Weather struct {
	// Endpoint is the forecast API to ask. Empty disables the lookup, and the
	// endpoint still answers — saying the button is off here, which is better
	// than a button that silently does nothing.
	Endpoint string
	// Timeout bounds one lookup. A tester is waiting on it with a form open, so
	// the budget is short: giving up and letting them type is a better outcome
	// than a spinner that outlasts their patience.
	Timeout time.Duration
}

// Blob configures the content-addressed store behind the images in a test log.
type Blob struct {
	Options blob.Options
	// MaxBytes caps a single upload.
	MaxBytes int64
	// OrphanGrace is how long an unreferenced blob is kept before the sweep
	// removes it. Images are uploaded while a form is still being filled in, so
	// a blob is unreachable for as long as it takes to finish writing the log —
	// the grace period is what keeps the sweep from deleting an image out from
	// under the tester who just pasted it.
	OrphanGrace time.Duration
}

// RateLimit configures per-caller token-bucket limits. Zero RPS disables
// the corresponding bucket. Burst defaults to 2× RPS when unset.
type RateLimit struct {
	ReadRPS    float64
	ReadBurst  int
	WriteRPS   float64
	WriteBurst int
}

// Enabled reports whether any rate limiting is configured.
func (r RateLimit) Enabled() bool {
	return r.ReadRPS > 0 || r.WriteRPS > 0
}

// Load reads the whole configuration from the environment.
//
// One loader per section, each building its own struct and checking its own
// values, so that a validation sits beside the thing it constrains rather than
// a hundred lines below it. This function is then a table of contents: what the
// server is configured by, in the order it is assembled.
func Load() (*Config, error) {
	cfg, err := loadServer()
	if err != nil {
		return nil, err
	}
	if cfg.Blob, err = loadBlob(); err != nil {
		return nil, err
	}
	if cfg.Weather, err = loadWeather(); err != nil {
		return nil, err
	}
	if cfg.APIKeys, err = loadAPIKeys(); err != nil {
		return nil, err
	}
	if cfg.Auth, err = loadAuth(); err != nil {
		return nil, err
	}
	cfg.RateLimit = loadRateLimit()
	return cfg, nil
}

// loadServer reads what the process itself needs: where the database is, what
// to listen on, and the bounds on a single request.
func loadServer() (*Config, error) {
	cfg := &Config{
		DatabaseURL:     envOrDefault("EVIDENCE_DATABASE_URL", "postgres://evidence:evidence@localhost:5432/evidence_store?sslmode=disable"),
		ListenAddr:      envOrDefault("EVIDENCE_LISTEN_ADDR", ":8000"),
		DefaultPageSize: envOrDefaultInt("EVIDENCE_DEFAULT_PAGE_SIZE", 100),
		MaxPageSize:     envOrDefaultInt("EVIDENCE_MAX_PAGE_SIZE", 1000),
		MaxBatchSize:    envOrDefaultInt("EVIDENCE_MAX_BATCH_SIZE", 1000),
		LogLevel:        envOrDefault("EVIDENCE_LOG_LEVEL", "INFO"),
		AnalyticsCacheTTL: time.Duration(
			envOrDefaultInt("EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS", 30)) * time.Second,
		// The older, analytics-only name still works and supplies the default,
		// so a deployment that set it before the budget covered search keeps
		// the value it chose.
		QueryTimeout: time.Duration(envOrDefaultInt("EVIDENCE_QUERY_TIMEOUT_SECONDS",
			envOrDefaultInt("EVIDENCE_ANALYTICS_QUERY_TIMEOUT_SECONDS", 15))) * time.Second,
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("EVIDENCE_DATABASE_URL is required")
	}
	if cfg.AnalyticsCacheTTL < 0 {
		return nil, fmt.Errorf("EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS must not be negative")
	}
	if cfg.QueryTimeout < 0 {
		return nil, fmt.Errorf("EVIDENCE_QUERY_TIMEOUT_SECONDS must not be negative")
	}
	return cfg, nil
}

// loadBlob reads where the images in a test log are kept.
func loadBlob() (Blob, error) {
	b := Blob{
		Options: blob.Options{
			Backend: envOrDefault("EVIDENCE_BLOB_BACKEND", "fs"),
			Path:    envOrDefault("EVIDENCE_BLOB_PATH", "blobs"),
			S3: blob.S3Config{
				Endpoint:  os.Getenv("EVIDENCE_BLOB_S3_ENDPOINT"),
				Bucket:    envOrDefault("EVIDENCE_BLOB_S3_BUCKET", "evidence-blobs"),
				AccessKey: os.Getenv("EVIDENCE_BLOB_S3_ACCESS_KEY"),
				SecretKey: os.Getenv("EVIDENCE_BLOB_S3_SECRET_KEY"),
				UseSSL:    os.Getenv("EVIDENCE_BLOB_S3_USE_SSL") == "true",
				Region:    os.Getenv("EVIDENCE_BLOB_S3_REGION"),
			},
		},
		// 5 MiB is a generous screenshot and a small photo. Videos will need
		// their own cap and a streaming upload path (#79).
		MaxBytes: int64(envOrDefaultInt("EVIDENCE_MAX_BLOB_BYTES", 5<<20)),
		OrphanGrace: time.Duration(
			envOrDefaultInt("EVIDENCE_BLOB_ORPHAN_GRACE_HOURS", 24)) * time.Hour,
	}

	if b.MaxBytes <= 0 {
		return Blob{}, fmt.Errorf("EVIDENCE_MAX_BLOB_BYTES must be positive")
	}
	if b.OrphanGrace < 0 {
		return Blob{}, fmt.Errorf("EVIDENCE_BLOB_ORPHAN_GRACE_HOURS must not be negative")
	}
	return b, nil
}

// loadWeather reads the lookup behind the Weather field on a manual record.
func loadWeather() (Weather, error) {
	w := Weather{
		// Open-Meteo needs no account and no key. That matters more than the
		// choice of service: a feature that only works once someone has signed
		// up for something is a feature most deployments never turn on.
		//
		// Read with LookupEnv rather than envOrDefault so that setting the
		// variable to nothing means nothing: an operator turning the lookup off
		// writes an empty value, and having that fall back to the default would
		// send the traffic they just tried to stop.
		Endpoint: envOrDefaultSet("EVIDENCE_WEATHER_ENDPOINT", "https://api.open-meteo.com/v1/forecast"),
		Timeout: time.Duration(
			envOrDefaultInt("EVIDENCE_WEATHER_TIMEOUT_SECONDS", 10)) * time.Second,
	}

	if w.Timeout <= 0 && w.Endpoint != "" {
		return Weather{}, fmt.Errorf("EVIDENCE_WEATHER_TIMEOUT_SECONDS must be positive")
	}
	return w, nil
}

// loadAPIKeys reads the static key list, which is empty in a deployment that
// authenticates against the principals table instead.
func loadAPIKeys() ([]APIKey, error) {
	raw := os.Getenv("EVIDENCE_API_KEYS")
	if raw == "" {
		return nil, nil
	}
	keys, err := ParseAPIKeys(raw)
	if err != nil {
		return nil, fmt.Errorf("EVIDENCE_API_KEYS: %w", err)
	}
	return keys, nil
}

// loadAuth reads who may call, and how they prove it.
func loadAuth() (Auth, error) {
	roleMap, err := loadRoleMap()
	if err != nil {
		return Auth{}, err
	}

	auth := Auth{
		DB:             os.Getenv("EVIDENCE_AUTH_DB") == "true",
		BootstrapAdmin: strings.TrimSpace(os.Getenv("EVIDENCE_BOOTSTRAP_ADMIN")),
		OIDC:           loadOIDC(roleMap),
		SAML:           loadSAML(),
		RoleMap:        roleMap,
		Session: Session{
			TTL: time.Duration(envOrDefaultInt("EVIDENCE_SESSION_TTL_HOURS", 12)) * time.Hour,
			// Secure unless explicitly turned off, so that forgetting the
			// variable fails towards the safe posture rather than away from it.
			CookieSecure: os.Getenv("EVIDENCE_COOKIE_SECURE") != "false",
		},
	}

	if err := validateAuth(auth); err != nil {
		return Auth{}, err
	}
	return auth, nil
}

func loadOIDC(roleMap map[string]string) OIDC {
	return OIDC{
		Issuer:       strings.TrimSpace(os.Getenv("EVIDENCE_OIDC_ISSUER")),
		ClientID:     strings.TrimSpace(os.Getenv("EVIDENCE_OIDC_CLIENT_ID")),
		ClientSecret: os.Getenv("EVIDENCE_OIDC_CLIENT_SECRET"),
		RedirectURL:  strings.TrimSpace(os.Getenv("EVIDENCE_OIDC_REDIRECT_URL")),
		Scopes:       splitAndTrim(envOrDefault("EVIDENCE_OIDC_SCOPES", "openid,profile,email")),
		GroupsClaim:  envOrDefault("EVIDENCE_OIDC_GROUPS_CLAIM", "groups"),
		RoleMap:      roleMap,
	}
}

func loadSAML() SAML {
	return SAML{
		IDPMetadataURL:  strings.TrimSpace(os.Getenv("EVIDENCE_SAML_IDP_METADATA_URL")),
		IDPMetadataFile: strings.TrimSpace(os.Getenv("EVIDENCE_SAML_IDP_METADATA_FILE")),
		RootURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("EVIDENCE_SAML_ROOT_URL")), "/"),
		EntityID:        strings.TrimSpace(os.Getenv("EVIDENCE_SAML_ENTITY_ID")),
		CertFile:        strings.TrimSpace(os.Getenv("EVIDENCE_SAML_CERT_FILE")),
		KeyFile:         strings.TrimSpace(os.Getenv("EVIDENCE_SAML_KEY_FILE")),
		// Defaults that match what most providers send when nobody has
		// configured a claim mapping at their end.
		EmailAttribute:  envOrDefault("EVIDENCE_SAML_EMAIL_ATTRIBUTE", "email"),
		NameAttribute:   envOrDefault("EVIDENCE_SAML_NAME_ATTRIBUTE", "displayName"),
		GroupsAttribute: envOrDefault("EVIDENCE_SAML_GROUPS_ATTRIBUTE", "groups"),
	}
}

// validateAuth checks the combinations, which is where authentication differs
// from every other section: the parts constrain each other. Both login front
// ends need somewhere for a session to resolve to, and that is the principals
// table EVIDENCE_AUTH_DB turns on.
func validateAuth(auth Auth) error {
	if auth.SAML.Enabled() {
		if auth.SAML.IDPMetadataURL != "" && auth.SAML.IDPMetadataFile != "" {
			return fmt.Errorf("set EVIDENCE_SAML_IDP_METADATA_URL or EVIDENCE_SAML_IDP_METADATA_FILE, not both")
		}
		for name, value := range map[string]string{
			"EVIDENCE_SAML_ROOT_URL":  auth.SAML.RootURL,
			"EVIDENCE_SAML_CERT_FILE": auth.SAML.CertFile,
			"EVIDENCE_SAML_KEY_FILE":  auth.SAML.KeyFile,
		} {
			if value == "" {
				return fmt.Errorf("%s is required when SAML is configured", name)
			}
		}
		// Same reason as OIDC: a session resolves to a principal, and without
		// the table there is nothing for a login to become.
		if !auth.DB {
			return fmt.Errorf("SAML requires EVIDENCE_AUTH_DB=true")
		}
		if auth.Session.TTL <= 0 {
			return fmt.Errorf("EVIDENCE_SESSION_TTL_HOURS must be positive")
		}
	}

	if auth.OIDC.Enabled() {
		// Half-configured SSO is worse than none: the login button exists and
		// every attempt to use it fails somewhere the operator cannot see.
		for name, value := range map[string]string{
			"EVIDENCE_OIDC_CLIENT_ID":    auth.OIDC.ClientID,
			"EVIDENCE_OIDC_REDIRECT_URL": auth.OIDC.RedirectURL,
		} {
			if value == "" {
				return fmt.Errorf("%s is required when EVIDENCE_OIDC_ISSUER is set", name)
			}
		}
		if auth.Session.TTL <= 0 {
			return fmt.Errorf("EVIDENCE_SESSION_TTL_HOURS must be positive")
		}
		// Sessions resolve to principals, which is the table EVIDENCE_AUTH_DB
		// turns on. Logging in without it would mint an identity nothing
		// consults.
		if !auth.DB {
			return fmt.Errorf("EVIDENCE_OIDC_ISSUER requires EVIDENCE_AUTH_DB=true")
		}
	}

	// Refuse rather than ignore. A subject named here and quietly dropped
	// leaves an operator waiting for a key that is never going to be logged,
	// on a store they have just locked themselves out of.
	if auth.BootstrapAdmin != "" && !auth.DB {
		return fmt.Errorf("EVIDENCE_BOOTSTRAP_ADMIN requires EVIDENCE_AUTH_DB=true")
	}

	return nil
}

// loadRoleMap reads the group-to-role mapping shared by both login front ends.
//
// EVIDENCE_OIDC_ROLE_MAP is still read, because it is what the OIDC release
// documented and an operator who set it should not have their groups quietly
// stop granting anything.
func loadRoleMap() (map[string]string, error) {
	name, raw := "EVIDENCE_GROUP_ROLE_MAP", os.Getenv("EVIDENCE_GROUP_ROLE_MAP")
	if raw == "" {
		name, raw = "EVIDENCE_OIDC_ROLE_MAP", os.Getenv("EVIDENCE_OIDC_ROLE_MAP")
	}
	roleMap, err := ParseRoleMap(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return roleMap, nil
}

// loadRateLimit reads the per-caller buckets. Zero RPS disables a bucket, which
// is the default, so this cannot fail.
func loadRateLimit() RateLimit {
	r := RateLimit{
		ReadRPS:    envOrDefaultFloat("EVIDENCE_RATE_LIMIT_READ_RPS", 0),
		WriteRPS:   envOrDefaultFloat("EVIDENCE_RATE_LIMIT_WRITE_RPS", 0),
		ReadBurst:  envOrDefaultInt("EVIDENCE_RATE_LIMIT_READ_BURST", 0),
		WriteBurst: envOrDefaultInt("EVIDENCE_RATE_LIMIT_WRITE_BURST", 0),
	}
	if r.ReadRPS > 0 && r.ReadBurst == 0 {
		r.ReadBurst = max(int(r.ReadRPS*2), 1)
	}
	if r.WriteRPS > 0 && r.WriteBurst == 0 {
		r.WriteBurst = max(int(r.WriteRPS*2), 1)
	}
	return r
}

// ParseAPIKeys parses a comma-separated list of "role:key" entries.
// Valid roles are "rw" (read-write) and "ro" (read-only).
func ParseAPIKeys(raw string) ([]APIKey, error) {
	var keys []APIKey
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		role, key, ok := strings.Cut(entry, ":")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid key entry %q: expected role:key (e.g. rw:my-secret)", entry)
		}
		switch role {
		case "rw":
			keys = append(keys, APIKey{Key: key, ReadOnly: false})
		case "ro":
			keys = append(keys, APIKey{Key: key, ReadOnly: true})
		default:
			return nil, fmt.Errorf("invalid role %q in entry %q: must be rw or ro", role, entry)
		}
	}
	return keys, nil
}

// ParseRoleMap reads "group:role,group:role" — which group at the identity
// provider grants which role here.
//
// Role names are not validated against the four this binary defines: that
// belongs to internal/auth, and this package is deliberately below it. An entry
// naming a role that does not exist grants nothing, which is the same thing a
// role_bindings row naming one does.
func ParseRoleMap(raw string) (map[string]string, error) {
	out := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		group, role, ok := strings.Cut(entry, ":")
		group, role = strings.TrimSpace(group), strings.TrimSpace(role)
		if !ok || group == "" || role == "" {
			return nil, fmt.Errorf("invalid entry %q: expected group:role (e.g. eng-leads:admin)", entry)
		}
		if existing, dup := out[group]; dup && existing != role {
			// Silently keeping one of them would hand somebody the wrong access
			// and give the operator nothing to look at.
			return nil, fmt.Errorf("group %q is mapped to both %q and %q", group, existing, role)
		}
		out[group] = role
	}
	return out, nil
}

func splitAndTrim(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envOrDefaultSet takes the variable at its word when it is set, including when
// it is set to nothing. For a setting whose empty value means "off", the
// difference between unset and empty is the whole point.
func envOrDefaultSet(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envOrDefaultFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
