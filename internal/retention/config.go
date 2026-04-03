package retention

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the retention configuration loaded from a YAML file.
type Config struct {
	Interval time.Duration `yaml:"interval"`
	Rules    []Rule        `yaml:"rules"`
}

// Rule defines a single retention rule with regex-based field matching.
type Rule struct {
	Name     string            `yaml:"name"`
	Match    map[string]string `yaml:"match"`
	MaxAge   time.Duration     `yaml:"max_age"`
	Priority int               `yaml:"priority"`
}

// validFields are the evidence fields that can be matched by retention rules.
var validFields = map[string]bool{
	"repo":          true,
	"branch":        true,
	"rcs_ref":       true,
	"procedure_ref": true,
	"evidence_type": true,
	"source":        true,
	"result":        true,
}

// LoadConfig reads and validates a retention config from a YAML file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read retention config: %w", err)
	}
	return ParseConfig(data)
}

// ParseConfig parses and validates retention config from YAML bytes.
func ParseConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse retention config: %w", err)
	}

	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}

	for i, rule := range cfg.Rules {
		if rule.Name == "" {
			return nil, fmt.Errorf("rule %d: name is required", i)
		}
		if rule.MaxAge < 0 {
			return nil, fmt.Errorf("rule %q: max_age must be >= 0", rule.Name)
		}
		for field, pattern := range rule.Match {
			if !validFields[field] {
				return nil, fmt.Errorf("rule %q: unknown match field %q", rule.Name, field)
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return nil, fmt.Errorf("rule %q: invalid regex for field %q: %w", rule.Name, field, err)
			}
		}
	}

	sort.Slice(cfg.Rules, func(i, j int) bool {
		return cfg.Rules[i].Priority > cfg.Rules[j].Priority
	})

	return &cfg, nil
}
