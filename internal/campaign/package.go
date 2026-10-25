package campaign

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nesono/evidence-store/internal/model"
	"gopkg.in/yaml.v3"
)

// Manifest describes reusable content. Build/release provenance (including the
// product revision) is associated with the archive digest outside this payload.
type Manifest struct {
	FormatVersion int             `json:"format_version"`
	Campaign      CampaignInfo    `json:"campaign"`
	Source        PackageSource   `json:"source"`
	Scenarios     []ScenarioEntry `json:"scenarios"`
}

type CampaignInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	PlannedLocation string `json:"planned_location,omitempty"`
	PlannedStart    string `json:"planned_start,omitempty"`
	PlannedEnd      string `json:"planned_end,omitempty"`
}

type PackageSource struct {
	Repository string `json:"repository"`
}

type ScenarioEntry struct {
	ProcedureRef string `json:"procedure_ref,omitempty"`
	Path         string `json:"path"`
	// ID is accepted only when reading a legacy v1 archive, then discarded.
	ID string `json:"id,omitempty"`
}

type Scenario struct {
	ID           string         `json:"id,omitempty"` // Legacy v1 input only.
	Title        string         `json:"title"`
	ProcedureRef string         `json:"procedure_ref"`
	Description  string         `json:"description"`
	Parameters   map[string]any `json:"parameters,omitempty"`
	Defaults     Defaults       `json:"defaults"`
	Assets       []string       `json:"assets,omitempty"`
	Traceability []TraceLink    `json:"traceability,omitempty"`
}

type Defaults struct {
	EvidenceType string         `json:"evidence_type"`
	Tags         []string       `json:"tags,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

// TraceLink identifies an authored specification entry, not a product build.
type TraceLink struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Repository   string `json:"repository"`
	Path         string `json:"path"`
	EntryVersion string `json:"entry_version,omitempty"`
}

type PackagedScenario struct {
	Definition Scenario          `json:"definition"`
	Procedure  *Procedure        `json:"procedure"`
	Assets     map[string]string `json:"assets,omitempty"` // archive path -> digest
}

type Package struct {
	Digest    string             `json:"digest"`
	Manifest  Manifest           `json:"manifest"`
	Scenarios []PackagedScenario `json:"scenarios"`
}

var packageID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// decodePackageYAML uses YAML's syntax with JSON's scalar types. In particular,
// numeric IDs and unquoted dates cannot silently become strings. Unknown schema
// keys are rejected; only explicit map fields (parameters/metadata) are open.
func decodePackageYAML(data []byte, name string, target any) error {
	if len(data) > MaxProcedureBytes || !utf8.Valid(data) || bytes.ContainsRune(data, '\x00') {
		return fmt.Errorf("%s: document must be UTF-8 without NUL and at most %d bytes", name, MaxProcedureBytes)
	}
	d := yaml.NewDecoder(bytes.NewReader(data))
	var doc, extra yaml.Node
	if err := d.Decode(&doc); err != nil {
		return fmt.Errorf("%s: invalid YAML: %w", name, err)
	}
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%s: expected exactly one YAML document", name)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s: expected a mapping", name)
	}
	if err := validateYAML(doc.Content[0], 1, 0); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := validateJSONScalars(doc.Content[0]); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var value any
	if err := doc.Decode(&value); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%s: YAML must contain JSON-compatible values: %w", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

func validateJSONScalars(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag != "!!str" && node.Tag != "!!bool" && node.Tag != "!!int" && node.Tag != "!!float" && node.Tag != "!!null" {
		return errorAt(node.Line, "unsupported scalar %s; quote dates and identifiers", node.Tag)
	}
	// A null is not a default object/string/list; omission is unambiguous.
	if node.Tag == "!!null" {
		return errorAt(node.Line, "null values are not supported; omit optional properties")
	}
	for _, child := range node.Content {
		if err := validateJSONScalars(child); err != nil {
			return err
		}
	}
	return nil
}

func parseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := decodePackageYAML(data, "manifest.yaml", &m); err != nil {
		return m, err
	}
	if m.FormatVersion != 1 && m.FormatVersion != 2 {
		return m, fmt.Errorf("unsupported package format_version %d", m.FormatVersion)
	}
	if !packageID.MatchString(m.Campaign.ID) || strings.TrimSpace(m.Campaign.Name) == "" {
		return m, fmt.Errorf("campaign requires a portable id and nonempty name")
	}
	if !repositoryURL(m.Source.Repository) {
		return m, fmt.Errorf("source.repository must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	if err := validatePlanDates(m.Campaign); err != nil {
		return m, err
	}
	return m, validateScenarioEntries(m.Scenarios, m.FormatVersion)
}

func validatePlanDates(c CampaignInfo) error {
	for _, date := range []string{c.PlannedStart, c.PlannedEnd} {
		if date == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return fmt.Errorf("planned dates must be quoted YYYY-MM-DD values: %w", err)
		}
	}
	if c.PlannedStart != "" && c.PlannedEnd != "" && c.PlannedEnd < c.PlannedStart {
		return fmt.Errorf("planned_end precedes planned_start")
	}
	return nil
}

func validateScenarioEntries(entries []ScenarioEntry, version int) error {
	if len(entries) == 0 {
		return fmt.Errorf("campaign must select at least one scenario")
	}
	ids, paths := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		key := entry.ProcedureRef
		if version == 1 {
			key = entry.ID
			if !packageID.MatchString(key) || entry.ProcedureRef != "" {
				return fmt.Errorf("v1 scenario entry requires a legacy id and no procedure_ref")
			}
		} else if entry.ID != "" || !validProcedureRef(key) {
			return fmt.Errorf("v2 scenario entry requires procedure_ref and no id")
		}
		if ids[key] {
			return fmt.Errorf("duplicate procedure identity %q", key)
		}
		if !archivePath(entry.Path) || entry.Path == "manifest.yaml" || paths[entry.Path] {
			return fmt.Errorf("invalid or duplicate scenario path %q", entry.Path)
		}
		ids[key], paths[entry.Path] = true, true
	}
	return nil
}

func parseScenario(data []byte, entry ScenarioEntry) (Scenario, error) {
	var s Scenario
	if err := decodePackageYAML(data, entry.Path, &s); err != nil {
		return s, err
	}
	if s.ID != entry.ID || strings.TrimSpace(s.Title) == "" || !validProcedureRef(s.ProcedureRef) {
		return s, fmt.Errorf("%s: title, procedure_ref, and matching legacy id (v1 only) are required", entry.Path)
	}
	if entry.ProcedureRef != "" && entry.ProcedureRef != s.ProcedureRef {
		return s, fmt.Errorf("%s: procedure_ref must match the manifest", entry.Path)
	}
	if !model.ValidEvidenceType(s.Defaults.EvidenceType) {
		return s, fmt.Errorf("%s: invalid defaults.evidence_type %q", entry.Path, s.Defaults.EvidenceType)
	}
	if _, err := relativeFile(entry.Path, s.Description); err != nil {
		return s, err
	}
	if err := validateDefaults(s.Defaults); err != nil {
		return s, fmt.Errorf("%s: %w", entry.Path, err)
	}
	for _, link := range s.Traceability {
		if err := validateTraceLink(link); err != nil {
			return s, fmt.Errorf("%s: %w", entry.Path, err)
		}
	}
	return s, nil
}

func validProcedureRef(ref string) bool {
	return ref != "" && strings.TrimSpace(ref) == ref && strings.IndexFunc(ref, unicode.IsControl) < 0
}

func validateDefaults(d Defaults) error {
	seen := map[string]bool{}
	for _, tag := range d.Tags {
		if strings.TrimSpace(tag) == "" || seen[tag] {
			return fmt.Errorf("tags must be nonempty and unique")
		}
		seen[tag] = true
	}
	for _, key := range []string{"campaign", "procedure_modified", "notes", "observations", "result", "finished_at", "source", "repo", "branch", "rcs_ref"} {
		if _, exists := d.Metadata[key]; exists {
			return fmt.Errorf("metadata default %q is reserved for execution", key)
		}
	}
	return nil
}

func validateTraceLink(link TraceLink) error {
	if link.Kind != "functional_scenario" && link.Kind != "logical_scenario" && link.Kind != "requirement" {
		return fmt.Errorf("invalid traceability kind %q", link.Kind)
	}
	file, _, _ := strings.Cut(link.Path, "#")
	if strings.TrimSpace(link.ID) == "" || !repositoryURL(link.Repository) || !archivePath(file) {
		return fmt.Errorf("traceability requires id, HTTPS repository, and repository-relative path")
	}
	return nil
}

func repositoryURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// Paths have one portable spelling, so duplicate detection also catches aliases.
func archivePath(value string) bool {
	return value != "" && value != "." && fs.ValidPath(value) && path.Clean(value) == value &&
		!strings.ContainsAny(value, "\\:%?#") && strings.IndexFunc(value, unicode.IsControl) < 0 && utf8.ValidString(value)
}

func relativeFile(owner, value string) (string, error) {
	if !archivePath(value) {
		return "", fmt.Errorf("%s: invalid relative file path %q", owner, value)
	}
	return path.Join(path.Dir(owner), value), nil
}
