package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds watch mode configuration.
type Config struct {
	APIURL       string
	APIKey       string
	Tags         []string
	PollInterval time.Duration
	DebounceWait time.Duration
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
		if err := parseConfig(data, &cfg); err != nil {
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

func parseConfig(data []byte, cfg *Config) error {
	for lineNo, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fmt.Errorf("line %d: expected key: value", lineNo+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "api_url":
			cfg.APIURL = unquote(value)
		case "api_key":
			cfg.APIKey = unquote(value)
		case "tags":
			cfg.Tags = parseTags(value)
		case "poll_interval":
			duration, err := time.ParseDuration(unquote(value))
			if err != nil {
				return fmt.Errorf("line %d: invalid poll_interval: %w", lineNo+1, err)
			}
			cfg.PollInterval = duration
		case "debounce_wait":
			duration, err := time.ParseDuration(unquote(value))
			if err != nil {
				return fmt.Errorf("line %d: invalid debounce_wait: %w", lineNo+1, err)
			}
			cfg.DebounceWait = duration
		default:
			return fmt.Errorf("line %d: unknown key %q", lineNo+1, key)
		}
	}
	return nil
}

func parseTags(value string) []string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(value, "["), "]"))
	}
	if value == "" {
		return nil
	}

	var tags []string
	for _, tag := range strings.Split(value, ",") {
		if tag = unquote(strings.TrimSpace(tag)); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func unquote(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
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
