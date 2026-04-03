package retention

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_Valid(t *testing.T) {
	yaml := `
interval: 12h
rules:
  - name: keep-releases
    match:
      branch: "^(main|release/.*)$"
    max_age: 0s
    priority: 100
  - name: default
    match: {}
    max_age: 2160h
    priority: 0
`
	cfg, err := ParseConfig([]byte(yaml))
	require.NoError(t, err)

	assert.Equal(t, 12*time.Hour, cfg.Interval)
	require.Len(t, cfg.Rules, 2)
	// Sorted by priority descending.
	assert.Equal(t, "keep-releases", cfg.Rules[0].Name)
	assert.Equal(t, 100, cfg.Rules[0].Priority)
	assert.Equal(t, "default", cfg.Rules[1].Name)
	assert.Equal(t, time.Duration(0), cfg.Rules[0].MaxAge)
	assert.Equal(t, 2160*time.Hour, cfg.Rules[1].MaxAge)
}

func TestParseConfig_DefaultInterval(t *testing.T) {
	yaml := `
rules:
  - name: default
    match: {}
    max_age: 720h
`
	cfg, err := ParseConfig([]byte(yaml))
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, cfg.Interval)
}

func TestParseConfig_InvalidRegex(t *testing.T) {
	yaml := `
rules:
  - name: bad
    match:
      branch: "[invalid"
    max_age: 24h
`
	_, err := ParseConfig([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid regex")
}

func TestParseConfig_UnknownField(t *testing.T) {
	yaml := `
rules:
  - name: bad
    match:
      nonexistent: ".*"
    max_age: 24h
`
	_, err := ParseConfig([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown match field")
}

func TestParseConfig_MissingName(t *testing.T) {
	yaml := `
rules:
  - match: {}
    max_age: 24h
`
	_, err := ParseConfig([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestParseConfig_NegativeMaxAge(t *testing.T) {
	yaml := `
rules:
  - name: bad
    match: {}
    max_age: -1h
`
	_, err := ParseConfig([]byte(yaml))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_age must be >= 0")
}

func TestParseConfig_SortsByPriority(t *testing.T) {
	yaml := `
rules:
  - name: low
    match: {}
    max_age: 24h
    priority: 10
  - name: high
    match: {}
    max_age: 24h
    priority: 90
  - name: mid
    match: {}
    max_age: 24h
    priority: 50
`
	cfg, err := ParseConfig([]byte(yaml))
	require.NoError(t, err)
	require.Len(t, cfg.Rules, 3)
	assert.Equal(t, "high", cfg.Rules[0].Name)
	assert.Equal(t, "mid", cfg.Rules[1].Name)
	assert.Equal(t, "low", cfg.Rules[2].Name)
}
