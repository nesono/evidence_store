package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, cfg.PollInterval)
	assert.Equal(t, 2*time.Second, cfg.DebounceWait)
	assert.Empty(t, cfg.APIURL)
}

func TestLoadConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	evidenceDir := filepath.Join(dir, ".evidence")
	require.NoError(t, os.MkdirAll(evidenceDir, 0o755))

	configYAML := `
api_url: https://evidence.example.com
api_key: my-secret
tags: [local, dev]
poll_interval: 10s
debounce_wait: 3s
`
	require.NoError(t, os.WriteFile(filepath.Join(evidenceDir, "config.yaml"), []byte(configYAML), 0o644))

	cfg, err := LoadConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "https://evidence.example.com", cfg.APIURL)
	assert.Equal(t, "my-secret", cfg.APIKey)
	assert.Equal(t, []string{"local", "dev"}, cfg.Tags)
	assert.Equal(t, 10*time.Second, cfg.PollInterval)
	assert.Equal(t, 3*time.Second, cfg.DebounceWait)
}

func TestLoadConfigEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	evidenceDir := filepath.Join(dir, ".evidence")
	require.NoError(t, os.MkdirAll(evidenceDir, 0o755))

	configYAML := `api_url: https://from-file.example.com`
	require.NoError(t, os.WriteFile(filepath.Join(evidenceDir, "config.yaml"), []byte(configYAML), 0o644))

	t.Setenv("EVIDENCE_STORE_URL", "https://from-env.example.com")
	t.Setenv("EVIDENCE_STORE_API_KEY", "env-key")

	cfg, err := LoadConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, "https://from-env.example.com", cfg.APIURL)
	assert.Equal(t, "env-key", cfg.APIKey)
}

func TestLoadConfigMissingFileUsesDefaults(t *testing.T) {
	dir := t.TempDir()
	// No .evidence/config.yaml — should not error.
	cfg, err := LoadConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, cfg.PollInterval)
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	evidenceDir := filepath.Join(dir, ".evidence")
	require.NoError(t, os.MkdirAll(evidenceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(evidenceDir, "config.yaml"), []byte("{{invalid"), 0o644))

	_, err := LoadConfig(dir)
	assert.Error(t, err)
}
