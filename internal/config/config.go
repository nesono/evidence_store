package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DatabaseURL     string
	ListenAddr      string
	DefaultPageSize int
	MaxPageSize     int
	MaxBatchSize    int
	LogLevel        string
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

	return cfg, nil
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
