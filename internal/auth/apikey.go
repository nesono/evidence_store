package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// KeyPrefix marks a string as an evidence store API key. It buys two things:
// a key is recognisable when it turns up somewhere it should not be — a log, a
// commit, a screenshot — and secret scanners can be taught one pattern.
const KeyPrefix = "evs_"

// keyBytes is the entropy behind a key. 256 bits is not a number anyone
// guesses, which is the assumption the fast hash in HashKey rests on; see the
// comment on principals.key_hash in migration 000006.
const keyBytes = 32

// GenerateKey mints an API key. The plaintext is returned once — to be shown to
// the operator and then forgotten — and only HashKey's digest is stored.
func GenerateKey() (string, error) {
	b := make([]byte, keyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return KeyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashKey returns the hex SHA-256 of a bearer token, which is what the
// principals table stores and looks up by.
//
// A fast hash is the right one here precisely because GenerateKey is the only
// way a key comes into being: there is no low-entropy secret to grind through,
// and authentication stays a single indexed equality check on a hot path. That
// stops being true the moment a caller may choose its own key.
func HashKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LooksLikeKey reports whether a token was minted by GenerateKey. It is a
// cheap filter, not a check: it lets the database authenticator skip a query
// for a credential that cannot be one of its keys — a legacy env-var key, or a
// session cookie's token in phase 5 — without saying anything about whether a
// key that does have the prefix is valid.
func LooksLikeKey(token string) bool {
	return strings.HasPrefix(token, KeyPrefix)
}
