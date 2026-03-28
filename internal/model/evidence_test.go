package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvidenceResultValid(t *testing.T) {
	valid := []EvidenceResult{ResultPass, ResultFail, ResultError, ResultSkipped}
	for _, r := range valid {
		assert.True(t, r.Valid(), "expected %q to be valid", r)
	}

	invalid := []EvidenceResult{"", "UNKNOWN", "pass", "Pass", "failing"}
	for _, r := range invalid {
		assert.False(t, r.Valid(), "expected %q to be invalid", r)
	}
}

func TestParseEvidenceResult(t *testing.T) {
	tests := []struct {
		input string
		want  EvidenceResult
		err   bool
	}{
		{"PASS", ResultPass, false},
		{"FAIL", ResultFail, false},
		{"ERROR", ResultError, false},
		{"SKIPPED", ResultSkipped, false},
		{"pass", "", true},
		{"", "", true},
		{"UNKNOWN", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseEvidenceResult(tt.input)
			if tt.err {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestEvidenceResultString(t *testing.T) {
	assert.Equal(t, "PASS", ResultPass.String())
	assert.Equal(t, "FAIL", ResultFail.String())
}
