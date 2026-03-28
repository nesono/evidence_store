package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

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
	FinishedAt   time.Time       `json:"finished_at"`
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
