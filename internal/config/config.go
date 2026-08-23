package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nesono/evidence-store/internal/blob"
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
	Auth            Auth
	RateLimit       RateLimit
	// AnalyticsCacheTTL is how long an aggregation is reused for an identical
	// filter. Zero disables caching. Sorting and paging are applied after the
	// query, so this mostly serves those without a round trip; the cost is that
	// a window can lag new evidence by up to this long.
	AnalyticsCacheTTL time.Duration
	// AnalyticsQueryTimeout bounds how long a single analytics aggregation may
	// run. Zero means no budget beyond the server's own request timeout.
	AnalyticsQueryTimeout time.Duration
	Blob                  Blob
	Weather               Weather
}

// Auth configures database-backed identities — principals with names, roles
// granted one at a time, and revocation that takes effect on the next request.
//
// It is off by default, and off is not the same as open: with DB set the store
// still honours APIKeys, and with neither the API is unauthenticated as it has
// always been.
type Auth struct {
	// DB turns on the principals table as a source of credentials. It never
	// falls open: once set, an empty table means nobody may in.
	DB bool
	// BootstrapAdmin is the subject of an administrator seeded on first start,
	// whose one-time key is logged. Without it a fresh database has no way in,
	// since issuing the first key is itself an administrator's operation.
	BootstrapAdmin string
}

// Weather configures the one lookup this store makes to a service outside it.
//
// It is a setting and not a constant because the deployments that most need
// weather on a test record are the ones on a proving ground behind a firewall:
// they need to point this at a mirror, at their own site's instruments, or at
// nothing at all.
type Weather struct {
	// Endpoint is the forecast API to ask. Empty disables the lookup, and the
	// endpoint still answers — saying the button is off here, which is better
	// than a button that silently does nothing.
	Endpoint string
	// Timeout bounds one lookup. A tester is waiting on it with a form open, so
	// the budget is short: giving up and letting them type is a better outcome
	// than a spinner that outlasts their patience.
	Timeout time.Duration
}

// Blob configures the content-addressed store behind the images in a test log.
type Blob struct {
	Options blob.Options
	// MaxBytes caps a single upload.
	MaxBytes int64
	// OrphanGrace is how long an unreferenced blob is kept before the sweep
	// removes it. Images are uploaded while a form is still being filled in, so
	// a blob is unreachable for as long as it takes to finish writing the log —
	// the grace period is what keeps the sweep from deleting an image out from
	// under the tester who just pasted it.
	OrphanGrace time.Duration
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
		AnalyticsQueryTimeout: time.Duration(
			envOrDefaultInt("EVIDENCE_ANALYTICS_QUERY_TIMEOUT_SECONDS", 15)) * time.Second,
	}

	cfg.Blob = Blob{
		Options: blob.Options{
			Backend: envOrDefault("EVIDENCE_BLOB_BACKEND", "fs"),
			Path:    envOrDefault("EVIDENCE_BLOB_PATH", "blobs"),
			S3: blob.S3Config{
				Endpoint:  os.Getenv("EVIDENCE_BLOB_S3_ENDPOINT"),
				Bucket:    envOrDefault("EVIDENCE_BLOB_S3_BUCKET", "evidence-blobs"),
				AccessKey: os.Getenv("EVIDENCE_BLOB_S3_ACCESS_KEY"),
				SecretKey: os.Getenv("EVIDENCE_BLOB_S3_SECRET_KEY"),
				UseSSL:    os.Getenv("EVIDENCE_BLOB_S3_USE_SSL") == "true",
				Region:    os.Getenv("EVIDENCE_BLOB_S3_REGION"),
			},
		},
		// 5 MiB is a generous screenshot and a small photo. Videos will need
		// their own cap and a streaming upload path (#79).
		MaxBytes: int64(envOrDefaultInt("EVIDENCE_MAX_BLOB_BYTES", 5<<20)),
		OrphanGrace: time.Duration(
			envOrDefaultInt("EVIDENCE_BLOB_ORPHAN_GRACE_HOURS", 24)) * time.Hour,
	}

	cfg.Weather = Weather{
		// Open-Meteo needs no account and no key. That matters more than the
		// choice of service: a feature that only works once someone has signed
		// up for something is a feature most deployments never turn on.
		//
		// Read with LookupEnv rather than envOrDefault so that setting the
		// variable to nothing means nothing: an operator turning the lookup off
		// writes an empty value, and having that fall back to the default would
		// send the traffic they just tried to stop.
		Endpoint: envOrDefaultSet("EVIDENCE_WEATHER_ENDPOINT", "https://api.open-meteo.com/v1/forecast"),
		Timeout: time.Duration(
			envOrDefaultInt("EVIDENCE_WEATHER_TIMEOUT_SECONDS", 10)) * time.Second,
	}

	if cfg.Weather.Timeout <= 0 && cfg.Weather.Endpoint != "" {
		return nil, fmt.Errorf("EVIDENCE_WEATHER_TIMEOUT_SECONDS must be positive")
	}

	if cfg.Blob.MaxBytes <= 0 {
		return nil, fmt.Errorf("EVIDENCE_MAX_BLOB_BYTES must be positive")
	}

	if cfg.Blob.OrphanGrace < 0 {
		return nil, fmt.Errorf("EVIDENCE_BLOB_ORPHAN_GRACE_HOURS must not be negative")
	}

	if cfg.AnalyticsCacheTTL < 0 {
		return nil, fmt.Errorf("EVIDENCE_ANALYTICS_CACHE_TTL_SECONDS must not be negative")
	}

	if cfg.AnalyticsQueryTimeout < 0 {
		return nil, fmt.Errorf("EVIDENCE_ANALYTICS_QUERY_TIMEOUT_SECONDS must not be negative")
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

	cfg.Auth = Auth{
		DB:             os.Getenv("EVIDENCE_AUTH_DB") == "true",
		BootstrapAdmin: strings.TrimSpace(os.Getenv("EVIDENCE_BOOTSTRAP_ADMIN")),
	}

	// Refuse rather than ignore. A subject named here and quietly dropped
	// leaves an operator waiting for a key that is never going to be logged,
	// on a store they have just locked themselves out of.
	if cfg.Auth.BootstrapAdmin != "" && !cfg.Auth.DB {
		return nil, fmt.Errorf("EVIDENCE_BOOTSTRAP_ADMIN requires EVIDENCE_AUTH_DB=true")
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

// envOrDefaultSet takes the variable at its word when it is set, including when
// it is set to nothing. For a setting whose empty value means "off", the
// difference between unset and empty is the whole point.
func envOrDefaultSet(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
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
