package auth

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateKeyIsPrefixedAndFullEntropy(t *testing.T) {
	key, err := GenerateKey()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(key, KeyPrefix), "key %q should carry the prefix", key)
	assert.True(t, LooksLikeKey(key))

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key, KeyPrefix))
	require.NoError(t, err, "the body should be raw base64url")
	// The fast hash in HashKey is only defensible if this stays 256 bits.
	assert.Len(t, raw, keyBytes)
}

func TestGenerateKeyDoesNotRepeat(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for range 100 {
		key, err := GenerateKey()
		require.NoError(t, err)
		_, dup := seen[key]
		require.False(t, dup, "GenerateKey returned %q twice", key)
		seen[key] = struct{}{}
	}
}

func TestHashKeyIsStableAndDistinguishing(t *testing.T) {
	a, err := GenerateKey()
	require.NoError(t, err)
	b, err := GenerateKey()
	require.NoError(t, err)

	assert.Equal(t, HashKey(a), HashKey(a), "the same token must hash to the same row key")
	assert.NotEqual(t, HashKey(a), HashKey(b))
	assert.NotContains(t, HashKey(a), a, "the digest must not carry the plaintext")
	assert.Len(t, HashKey(a), 64, "hex SHA-256")
}

func TestLooksLikeKeyRejectsOtherCredentials(t *testing.T) {
	// A legacy env-var key and, later, whatever a session carries: both reach
	// the same header, and neither should cost a database round trip.
	for _, token := range []string{"", "my-secret-key", "rw:my-secret-key", "EVS_upper", "evs-dash"} {
		assert.False(t, LooksLikeKey(token), "%q should not look like a minted key", token)
	}
}
