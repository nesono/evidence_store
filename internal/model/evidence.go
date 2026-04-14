package model

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// FlexibleTime accepts multiple datetime formats during JSON unmarshalling
// and normalizes them to UTC. Supported formats:
//   - RFC3339 / RFC3339Nano (with timezone)
//   - "2006-01-02T15:04:05" (zoneless, treated as UTC)
//   - "2006-01-02 15:04:05" (zoneless, treated as UTC)
//   - "2006-01-02 15:04"    (zoneless, treated as UTC)
//   - "2006-01-02"          (date only, 00:00:00 UTC)
type FlexibleTime struct {
	time.Time
}

var flexibleTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

// ParseFlexibleTime parses a datetime string in any of the supported formats
// and returns the result normalized to UTC.
func ParseFlexibleTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range flexibleTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized datetime %q (expected RFC3339 or YYYY-MM-DD[ HH:MM[:SS]])", s)
}

func (ft *FlexibleTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := ParseFlexibleTime(s)
	if err != nil {
		return err
	}
	ft.Time = t
	return nil
}

func (ft FlexibleTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(ft.Time.UTC())
}

type EvidenceResult string

const (
	ResultPass    EvidenceResult = "PASS"
	ResultFail    EvidenceResult = "FAIL"
	ResultError   EvidenceResult = "ERROR"
	ResultSkipped EvidenceResult = "SKIPPED"
)

func (r EvidenceResult) Valid() bool {
	switch r {
	case ResultPass, ResultFail, ResultError, ResultSkipped:
		return true
	}
	return false
}

type Evidence struct {
	ID           uuid.UUID       `json:"id"`
	Repo         string          `json:"repo"`
	Branch       string          `json:"branch"`
	RCSRef       string          `json:"rcs_ref"`
	ProcedureRef string          `json:"procedure_ref"`
	EvidenceType string          `json:"evidence_type"`
	Source       string          `json:"source"`
	Result       EvidenceResult  `json:"result"`
	FinishedAt   time.Time       `json:"finished_at"`
	IngestedAt   time.Time       `json:"ingested_at"`
	Metadata     json.RawMessage `json:"metadata"`
}

type EvidenceCreate struct {
	Repo         string          `json:"repo"`
	Branch       string          `json:"branch"`
	RCSRef       string          `json:"rcs_ref"`
	ProcedureRef string          `json:"procedure_ref"`
	EvidenceType string          `json:"evidence_type"`
	Source       string          `json:"source"`
	Result       EvidenceResult  `json:"result"`
	FinishedAt   FlexibleTime    `json:"finished_at"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

// EvidenceResponse wraps Evidence with optional inheritance info.
type EvidenceResponse struct {
	Evidence
	Inherited              *bool      `json:"inherited,omitempty"`
	InheritanceDeclaration *uuid.UUID `json:"inheritance_declaration_id,omitempty"`
}

type EvidenceFilter struct {
	Repo           *string
	RCSRef         *string
	Branch         *string
	EvidenceType   *string
	Result         []EvidenceResult
	Source         *string
	ProcedureRef   *string
	FinishedAfter  *time.Time
	FinishedBefore *time.Time
	Tags           []string
	Notes          *string
}

type BatchRequest struct {
	Records []EvidenceCreate `json:"records"`
}

type BatchRecordStatus struct {
	Index  int       `json:"index"`
	ID     uuid.UUID `json:"id,omitempty"`
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
}

type BatchResponse struct {
	Results []BatchRecordStatus `json:"results"`
}

func (r EvidenceResult) String() string {
	return string(r)
}

func ParseEvidenceResult(s string) (EvidenceResult, error) {
	r := EvidenceResult(s)
	if !r.Valid() {
		return "", fmt.Errorf("invalid result: %q, must be one of PASS, FAIL, ERROR, SKIPPED", s)
	}
	return r, nil
}
