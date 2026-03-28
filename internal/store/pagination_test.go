package store

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 3, 28, 10, 0, 0, 0, time.UTC)
	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")

	encoded := EncodeCursor(ts, id)
	assert.NotEmpty(t, encoded)

	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.True(t, ts.Equal(decoded.IngestedAt), "timestamps should match")
	assert.Equal(t, id, decoded.ID)
}

func TestCursorDifferentValuesProduceDifferentStrings(t *testing.T) {
	ts := time.Now().UTC()
	id1 := uuid.New()
	id2 := uuid.New()

	c1 := EncodeCursor(ts, id1)
	c2 := EncodeCursor(ts, id2)
	assert.NotEqual(t, c1, c2)
}

func TestDecodeCursorInvalid(t *testing.T) {
	tests := []string{
		"",
		"not-base64!!!",
		"dGhpcyBpcyBub3QganNvbg==", // valid base64 but not valid JSON cursor
	}

	for _, s := range tests {
		t.Run(s, func(t *testing.T) {
			_, err := DecodeCursor(s)
			assert.Error(t, err)
		})
	}
}

func TestCursorPreservesSubSecondPrecision(t *testing.T) {
	ts := time.Date(2026, 3, 28, 10, 0, 0, 123456000, time.UTC) // with microseconds
	id := uuid.New()

	encoded := EncodeCursor(ts, id)
	decoded, err := DecodeCursor(encoded)
	require.NoError(t, err)
	assert.True(t, ts.Equal(decoded.IngestedAt))
}
