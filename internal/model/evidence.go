package model

import (
	"encoding/json"
	"fmt"
	"slices"
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

// How the evidence was collected. This is a closed set of three, and it is the
// field that tells a reader what a record's metadata means (DESIGN.md §2.2).
//
// It used to be any lowercase word, which produced `bazel`, `pytest`, `gotest`
// and `junit` — four spellings of "a machine ran it" that no query could treat
// as one thing, and no reader could tell apart from a category of test. Which
// runner produced a record is still worth knowing, so it lives in
// `metadata.collector` where it does not have to carry that meaning too.
const (
	// TypeCI is anything a machine ran unattended: a pipeline, a watch mode, a
	// developer's `bazel test`. Nobody was watching it happen.
	TypeCI = "ci"
	// TypeManualTest is a procedure a person carried out and reported.
	TypeManualTest = "manual_test"
	// TypeDemonstration is evidence produced by showing the thing working —
	// to a customer, an auditor, or a room — rather than by testing it.
	TypeDemonstration = "demonstration"
)

// EvidenceTypes is the allowed set, in the order a UI should offer it.
var EvidenceTypes = []string{TypeCI, TypeManualTest, TypeDemonstration}

// ValidEvidenceType reports whether s is one of the three collection methods.
func ValidEvidenceType(s string) bool {
	return slices.Contains(EvidenceTypes, s)
}

type Evidence struct {
	ID uuid.UUID `json:"id"`
	// ClientRecordID is the token the client chose for this submission, if it
	// sent one. Returned so that a client reconciling a queue can match what it
	// sent against what the store has.
	ClientRecordID *uuid.UUID      `json:"client_record_id,omitempty"`
	Repo           string          `json:"repo"`
	Branch         string          `json:"branch"`
	RCSRef         string          `json:"rcs_ref"`
	ProcedureRef   string          `json:"procedure_ref"`
	EvidenceType   string          `json:"evidence_type"`
	Source         string          `json:"source"`
	Result         EvidenceResult  `json:"result"`
	FinishedAt     time.Time       `json:"finished_at"`
	IngestedAt     time.Time       `json:"ingested_at"`
	Metadata       json.RawMessage `json:"metadata"`
}

type EvidenceCreate struct {
	// ClientRecordID is a UUID the client mints for the submission, not for the
	// record: the store still chooses the id. Sending the same one twice files
	// one record, which is what lets a client retry a post whose response it
	// never saw. See docs/offline-support-plan.md.
	//
	// A string rather than a uuid.UUID because a malformed value is a fact
	// about one field, and this way the client is told that, in a 422 alongside
	// any other field it got wrong, rather than having the whole body refused
	// as unparseable JSON.
	ClientRecordID *string         `json:"client_record_id,omitempty"`
	Repo           string          `json:"repo"`
	Branch         string          `json:"branch"`
	RCSRef         string          `json:"rcs_ref"`
	ProcedureRef   string          `json:"procedure_ref"`
	EvidenceType   string          `json:"evidence_type"`
	Source         string          `json:"source"`
	Result         EvidenceResult  `json:"result"`
	FinishedAt     FlexibleTime    `json:"finished_at"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

// EvidenceResponse wraps Evidence with optional inheritance info.
type EvidenceResponse struct {
	Evidence
	Inherited              *bool      `json:"inherited,omitempty"`
	InheritanceDeclaration *uuid.UUID `json:"inheritance_declaration_id,omitempty"`
}

type EvidenceFilter struct {
	Repo   *string
	RCSRef *string
	Branch *string
	// Ref matches a branch, tag or commit with one value. The UI offers a single
	// box because a user pasting "what they are looking at" does not want to
	// first classify it.
	Ref            *string
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

// The statuses a record in a batch can come back with.
const (
	// StatusCreated: this call is what stored it.
	StatusCreated = "created"
	// StatusDuplicate: the store already had this submission, under the
	// client_record_id it was sent with, and the id names the record it became.
	// Not an error — the client asked what happened to something it sent, and
	// this is the answer.
	StatusDuplicate = "duplicate"
	// StatusError: the record was not stored and will not be until it changes.
	StatusError = "error"
)

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
