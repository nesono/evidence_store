package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	RateLimit       RateLimit
	// AnalyticsCacheTTL is how long an aggregation is reused for an identical
	// filter. Zero disables caching. Sorting and paging are applied after the
	// query, so this mostly serves those without a round trip; the cost is that
	// a window can lag new evidence by up to this long.
	AnalyticsCacheTTL time.Duration
}

// RateLimit configures per-caller token-bucket limits. Zero RPS disables
// the corresponding bucket. Burst defaults to 2× RPS when unset.
type RateLimit struct {
	ReadRPS    float64
	ReadBurst  int
	WriteRPS   float64
	WriteBurst int
}

// Enabled reports whether any rate limiting is configured.
func (r RateLimit) Enabled() bool {
	return r.ReadRPS > 0 || r.WriteRPS > 0
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:     envOrDefault("EVIDENCE_DATABASE_URL", "postgres://evidence:evidence@localhost:5432/evidence_store?sslmode=disable"),
		ListenAddr:      envOrDefault("EVIDENCE_LISTEN_ADDR", ":8000"),
		DefaultPageSize: envOrDefaultInt("EVIDENCE_DEFAULT_PAGE_SIZE", 100),
		MaxPageSize:     envOrDefaultInt("EVIDENCE_MAX_PAGE_SIZE", 1000),
		MaxBatchSize:    envOrDefaultInt("EVIDENCE_MAX_BATCH_SIZE", 1000),
		LogLevel:        envOrDefault("EVIDENCE_LOG_LEVEL", "INFO"),
		AnalyticsCacheTTL: time.Duration(
			envOrDefaultInt("EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS", 30)) * time.Second,
	}

	if cfg.AnalyticsCacheTTL < 0 {
		return nil, fmt.Errorf("EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS must not be negative")
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

	cfg.RateLimit = RateLimit{
		ReadRPS:    envOrDefaultFloat("EVIDENCE_RATE_LIMIT_READ_RPS", 0),
		WriteRPS:   envOrDefaultFloat("EVIDENCE_RATE_LIMIT_WRITE_RPS", 0),
		ReadBurst:  envOrDefaultInt("EVIDENCE_RATE_LIMIT_READ_BURST", 0),
		WriteBurst: envOrDefaultInt("EVIDENCE_RATE_LIMIT_WRITE_BURST", 0),
	}
	if cfg.RateLimit.ReadRPS > 0 && cfg.RateLimit.ReadBurst == 0 {
		cfg.RateLimit.ReadBurst = max(int(cfg.RateLimit.ReadRPS*2), 1)
	}
	if cfg.RateLimit.WriteRPS > 0 && cfg.RateLimit.WriteBurst == 0 {
		cfg.RateLimit.WriteBurst = max(int(cfg.RateLimit.WriteRPS*2), 1)
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

func envOrDefaultFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}
