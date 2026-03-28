package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type InheritanceDeclaration struct {
	ID             uuid.UUID       `json:"id"`
	CreatedAt      time.Time       `json:"created_at"`
	Repo           string          `json:"repo"`
	SourceRCSRef   string          `json:"source_rcs_ref"`
	TargetRCSRef   string          `json:"target_rcs_ref"`
	Scope          json.RawMessage `json:"scope"`
	Justification  string          `json:"justification"`
	CreatedBy      string          `json:"created_by"`
}

type InheritanceCreate struct {
	Repo          string          `json:"repo"`
	SourceRCSRef  string          `json:"source_rcs_ref"`
	TargetRCSRef  string          `json:"target_rcs_ref"`
	Scope         json.RawMessage `json:"scope"`
	Justification string          `json:"justification"`
	CreatedBy     string          `json:"created_by"`
}

type InheritanceFilter struct {
	Repo         *string
	SourceRCSRef *string
	TargetRCSRef *string
}
