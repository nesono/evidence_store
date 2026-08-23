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
