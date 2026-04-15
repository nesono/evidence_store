package config

import (
	"testing"

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
