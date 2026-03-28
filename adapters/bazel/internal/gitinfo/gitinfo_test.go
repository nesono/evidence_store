package gitinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRepoPattern(t *testing.T) {
	tests := []struct {
		url  string
		repo string
	}{
		{"https://github.com/myorg/firmware.git", "myorg/firmware"},
		{"https://github.com/myorg/firmware", "myorg/firmware"},
		{"git@github.com:myorg/firmware.git", "myorg/firmware"},
		{"git@github.com:myorg/firmware", "myorg/firmware"},
		{"ssh://git@gitlab.example.com:2222/team/project.git", "team/project"},
		{"https://gitlab.com/team/project", "team/project"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			matches := repoPattern.FindStringSubmatch(tt.url)
			if assert.Len(t, matches, 2) {
				assert.Equal(t, tt.repo, matches[1])
			}
		})
	}
}

func TestDetect(t *testing.T) {
	// This test runs in the evidence_store repo, so it should succeed.
	info, err := Detect()
	if err != nil {
		t.Skip("not in a git repo, skipping")
	}

	assert.NotEmpty(t, info.Ref)
	assert.NotEmpty(t, info.Branch)
	assert.NotEmpty(t, info.Repo)
}

func TestDetectSource(t *testing.T) {
	source := DetectSource()
	assert.NotEmpty(t, source)
}
