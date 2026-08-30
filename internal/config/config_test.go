package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAPIKeysValid(t *testing.T) {
	keys, err := ParseAPIKeys("rw:my-secret,ro:read-only-key")
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Equal(t, APIKey{Key: "my-secret", ReadOnly: false}, keys[0])
	assert.Equal(t, APIKey{Key: "read-only-key", ReadOnly: true}, keys[1])
}

func TestParseAPIKeysSingle(t *testing.T) {
	keys, err := ParseAPIKeys("rw:only-key")
	require.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.Equal(t, "only-key", keys[0].Key)
	assert.False(t, keys[0].ReadOnly)
}

func TestParseAPIKeysWithSpaces(t *testing.T) {
	keys, err := ParseAPIKeys("  rw:key1 , ro:key2  ")
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

func TestParseAPIKeysColonInKey(t *testing.T) {
	// Key itself can contain colons.
	keys, err := ParseAPIKeys("rw:my:secret:key")
	require.NoError(t, err)
	assert.Equal(t, "my:secret:key", keys[0].Key)
}

func TestParseAPIKeysEmpty(t *testing.T) {
	keys, err := ParseAPIKeys("")
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestParseAPIKeysInvalidRole(t *testing.T) {
	_, err := ParseAPIKeys("admin:key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid role")
}

func TestParseAPIKeysMissingKey(t *testing.T) {
	_, err := ParseAPIKeys("rw:")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid key entry")
}

func TestParseAPIKeysNoColon(t *testing.T) {
	_, err := ParseAPIKeys("justaplainkey")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid key entry")
}

// --- Weather ---

// The lookup works out of the box, because a service that needs no account is
// only useful if nobody has to configure it first.
func TestWeatherDefaults(t *testing.T) {
	cfg, err := Load()
	require.NoError(t, err)
	assert.Contains(t, cfg.Weather.Endpoint, "open-meteo.com")
	assert.Equal(t, 10*time.Second, cfg.Weather.Timeout)
}

func TestWeatherEndpointIsConfigurable(t *testing.T) {
	t.Setenv("EVIDENCE_WEATHER_ENDPOINT", "http://weather.internal/v1/forecast")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "http://weather.internal/v1/forecast", cfg.Weather.Endpoint)
}

// An operator turning the lookup off writes an empty value, and an empty value
// that fell back to the default would send exactly the traffic they set out to
// stop.
func TestWeatherEmptyEndpointDisablesTheLookup(t *testing.T) {
	t.Setenv("EVIDENCE_WEATHER_ENDPOINT", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.Weather.Endpoint)
}

// A tester is waiting on the lookup with a form open, so a budget of zero is
// not "no limit" here — it is a spinner that never ends.
func TestWeatherTimeoutMustBePositive(t *testing.T) {
	t.Setenv("EVIDENCE_WEATHER_TIMEOUT_SECONDS", "0")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EVIDENCE_WEATHER_TIMEOUT_SECONDS")
}

// With the lookup off there is nothing to time out, so a budget nobody will
// spend is not worth refusing to start over.
func TestWeatherTimeoutIsMootWhenDisabled(t *testing.T) {
	t.Setenv("EVIDENCE_WEATHER_ENDPOINT", "")
	t.Setenv("EVIDENCE_WEATHER_TIMEOUT_SECONDS", "0")

	_, err := Load()
	assert.NoError(t, err)
}

// --- The combinations authentication will not accept (issue #126) ---
//
// These rules were previously buried a hundred lines inside Load and could only
// be reached by setting environment variables. Splitting validation out made
// them testable directly, which is the point of the split — every rule below is
// a way an operator can end up with a login button that cannot work.

func validAuth() Auth {
	return Auth{
		DB:      true,
		Session: Session{TTL: 12 * time.Hour},
	}
}

func withOIDC(auth Auth) Auth {
	auth.OIDC = OIDC{
		Issuer:      "https://idp.example.com",
		ClientID:    "evidence-store",
		RedirectURL: "https://evidence.example.com/auth/callback",
	}
	return auth
}

func withSAML(auth Auth) Auth {
	auth.SAML = SAML{
		IDPMetadataURL: "https://idp.example.com/metadata",
		RootURL:        "https://evidence.example.com",
		CertFile:       "/etc/evidence/sp.crt",
		KeyFile:        "/etc/evidence/sp.key",
	}
	return auth
}

func TestAuthWithNoLoginConfiguredIsFine(t *testing.T) {
	// The default deployment: API keys only, no SSO at all.
	assert.NoError(t, validateAuth(Auth{}))
}

func TestOIDCNeedsItsOwnSettings(t *testing.T) {
	// Half-configured SSO is worse than none: the button exists and every
	// attempt to use it fails somewhere the operator cannot see.
	for _, tt := range []struct {
		name   string
		mutate func(*Auth)
		want   string
	}{
		{"no client id", func(a *Auth) { a.OIDC.ClientID = "" }, "EVIDENCE_OIDC_CLIENT_ID"},
		{"no redirect url", func(a *Auth) { a.OIDC.RedirectURL = "" }, "EVIDENCE_OIDC_REDIRECT_URL"},
		{"no principals table", func(a *Auth) { a.DB = false }, "EVIDENCE_AUTH_DB"},
		{"no session lifetime", func(a *Auth) { a.Session.TTL = 0 }, "EVIDENCE_SESSION_TTL_HOURS"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := withOIDC(validAuth())
			tt.mutate(&auth)
			err := validateAuth(auth)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tt.want)
			}
		})
	}
}

func TestSAMLNeedsItsOwnSettings(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*Auth)
		want   string
	}{
		{"no root url", func(a *Auth) { a.SAML.RootURL = "" }, "EVIDENCE_SAML_ROOT_URL"},
		{"no certificate", func(a *Auth) { a.SAML.CertFile = "" }, "EVIDENCE_SAML_CERT_FILE"},
		{"no key", func(a *Auth) { a.SAML.KeyFile = "" }, "EVIDENCE_SAML_KEY_FILE"},
		{"no principals table", func(a *Auth) { a.DB = false }, "EVIDENCE_AUTH_DB"},
		{"no session lifetime", func(a *Auth) { a.Session.TTL = 0 }, "EVIDENCE_SESSION_TTL_HOURS"},
		{
			"metadata from two places at once",
			func(a *Auth) { a.SAML.IDPMetadataFile = "/etc/evidence/idp.xml" },
			"not both",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			auth := withSAML(validAuth())
			tt.mutate(&auth)
			err := validateAuth(auth)
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tt.want)
			}
		})
	}
}

func TestBothFrontEndsAtOnceIsAllowed(t *testing.T) {
	// A company moving between protocols has a period where each is somebody's
	// way in.
	assert.NoError(t, validateAuth(withSAML(withOIDC(validAuth()))))
}

func TestBootstrapAdminNeedsSomewhereToLand(t *testing.T) {
	// Refusing rather than ignoring: a subject named here and quietly dropped
	// leaves an operator waiting for a key that is never going to be logged.
	auth := Auth{BootstrapAdmin: "user:admin@example.com"}
	err := validateAuth(auth)
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "EVIDENCE_AUTH_DB")
	}

	auth.DB = true
	assert.NoError(t, validateAuth(auth))
}

// --- Bounds the rest of the sections enforce ---

func TestBlobBounds(t *testing.T) {
	t.Setenv("EVIDENCE_MAX_BLOB_BYTES", "0")
	_, err := loadBlob()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "EVIDENCE_MAX_BLOB_BYTES")
	}

	t.Setenv("EVIDENCE_MAX_BLOB_BYTES", "1024")
	t.Setenv("EVIDENCE_BLOB_ORPHAN_GRACE_HOURS", "-1")
	_, err = loadBlob()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "EVIDENCE_BLOB_ORPHAN_GRACE_HOURS")
	}
}

func TestQueryTimeoutFallsBackToTheOlderName(t *testing.T) {
	// A deployment that set the analytics-only name before the budget covered
	// search keeps the value it chose.
	t.Setenv("EVIDENCE_ANALYTICS_QUERY_TIMEOUT_SECONDS", "42")
	cfg, err := loadServer()
	require.NoError(t, err)
	assert.Equal(t, 42*time.Second, cfg.QueryTimeout)

	// And the general name wins when both are set.
	t.Setenv("EVIDENCE_QUERY_TIMEOUT_SECONDS", "7")
	cfg, err = loadServer()
	require.NoError(t, err)
	assert.Equal(t, 7*time.Second, cfg.QueryTimeout)
}

func TestNegativeBudgetsAreRefused(t *testing.T) {
	t.Setenv("EVIDENCE_QUERY_TIMEOUT_SECONDS", "-1")
	_, err := loadServer()
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "EVIDENCE_QUERY_TIMEOUT_SECONDS")
	}
}
