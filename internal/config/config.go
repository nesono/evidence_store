package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// APIKey represents a configured API key with its access role.
type APIKey struct {
	Key      string
	ReadOnly bool
}

type Config struct {
	DatabaseURL     string
	ListenAddr      string
	DefaultPageSize int
	MaxPageSize     int
	MaxBatchSize    int
	LogLevel        string
	APIKeys         []APIKey
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:     envOrDefault("EVIDENCE_DATABASE_URL", "postgres://evidence:evidence@localhost:5432/evidence_store?sslmode=disable"),
		ListenAddr:      envOrDefault("EVIDENCE_LISTEN_ADDR", ":8000"),
		DefaultPageSize: envOrDefaultInt("EVIDENCE_DEFAULT_PAGE_SIZE", 100),
		MaxPageSize:     envOrDefaultInt("EVIDENCE_MAX_PAGE_SIZE", 1000),
		MaxBatchSize:    envOrDefaultInt("EVIDENCE_MAX_BATCH_SIZE", 1000),
		LogLevel:        envOrDefault("EVIDENCE_LOG_LEVEL", "INFO"),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("EVIDENCE_DATABASE_URL is required")
	}

	if raw := os.Getenv("EVIDENCE_API_KEYS"); raw != "" {
		keys, err := ParseAPIKeys(raw)
		if err != nil {
			return nil, fmt.Errorf("EVIDENCE_API_KEYS: %w", err)
		}
		cfg.APIKeys = keys
	}

	return cfg, nil
}

// ParseAPIKeys parses a comma-separated list of "role:key" entries.
// Valid roles are "rw" (read-write) and "ro" (read-only).
func ParseAPIKeys(raw string) ([]APIKey, error) {
	var keys []APIKey
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		role, key, ok := strings.Cut(entry, ":")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid key entry %q: expected role:key (e.g. rw:my-secret)", entry)
		}
		switch role {
		case "rw":
			keys = append(keys, APIKey{Key: key, ReadOnly: false})
		case "ro":
			keys = append(keys, APIKey{Key: key, ReadOnly: true})
		default:
			return nil, fmt.Errorf("invalid role %q in entry %q: must be rw or ro", role, entry)
		}
	}
	return keys, nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
