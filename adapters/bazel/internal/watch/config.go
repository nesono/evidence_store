package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds watch mode configuration.
type Config struct {
	APIURL       string        `yaml:"api_url"`
	APIKey       string        `yaml:"api_key"`
	Tags         []string      `yaml:"tags"`
	PollInterval time.Duration `yaml:"poll_interval"`
	DebounceWait time.Duration `yaml:"debounce_wait"`
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		PollInterval: 5 * time.Second,
		DebounceWait: 2 * time.Second,
	}
}

// LoadConfigFile loads config from .evidence/config.yaml in the given
// workspace directory. It does NOT consult environment variables, so the
// YAML is the single source of truth. Missing file is not an error.
func LoadConfigFile(workspaceDir string) (Config, error) {
	cfg := DefaultConfig()

	configPath := filepath.Join(workspaceDir, ".evidence", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err == nil {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return cfg, fmt.Errorf("read %s: %w", configPath, err)
	}

	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.DebounceWait <= 0 {
		cfg.DebounceWait = 2 * time.Second
	}

	return cfg, nil
}

// LoadConfig loads config from .evidence/config.yaml, then overlays
// EVIDENCE_STORE_URL and EVIDENCE_STORE_API_KEY env vars. Used by the
// watch subcommand for backwards compatibility with pre-file deployments.
func LoadConfig(workspaceDir string) (Config, error) {
	cfg, err := LoadConfigFile(workspaceDir)
	if err != nil {
		return cfg, err
	}
	if v := os.Getenv("EVIDENCE_STORE_URL"); v != "" {
		cfg.APIURL = v
	}
	if v := os.Getenv("EVIDENCE_STORE_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	return cfg, nil
}

// EvidenceDir returns the .evidence directory path for the given workspace.
func EvidenceDir(workspaceDir string) string {
	return filepath.Join(workspaceDir, ".evidence")
}

// EnsureEvidenceDir creates the .evidence directory if it doesn't exist.
func EnsureEvidenceDir(workspaceDir string) error {
	return os.MkdirAll(EvidenceDir(workspaceDir), 0o755)
}
