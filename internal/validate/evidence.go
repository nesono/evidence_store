package validate

import (
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/nesono/evidence-store/internal/model"
)

func EvidenceCreate(e *model.EvidenceCreate) []string {
	var errs []string

	if e.Repo == "" {
		errs = append(errs, "repo is required")
	}
	if e.Branch == "" {
		errs = append(errs, "branch is required")
	}
	if e.RCSRef == "" {
		errs = append(errs, "rcs_ref is required")
	}
	if e.ProcedureRef == "" {
		errs = append(errs, "procedure_ref is required")
	}
	if e.EvidenceType == "" {
		errs = append(errs, "evidence_type is required")
	} else if !model.ValidEvidenceType(e.EvidenceType) {
		// Naming the three matters more than naming the rule: a client sending
		// `bazel` learns nothing from a pattern, and every rejected value here
		// is a client that has to be changed to send something else.
		errs = append(errs, fmt.Sprintf("evidence_type %q is invalid, must be one of %s",
			e.EvidenceType, strings.Join(model.EvidenceTypes, ", ")))
	}
	if e.Source == "" {
		errs = append(errs, "source is required")
	}
	if !e.Result.Valid() {
		errs = append(errs, fmt.Sprintf("result %q is invalid, must be one of PASS, FAIL, ERROR, SKIPPED", e.Result))
	}
	if e.FinishedAt.Time.IsZero() {
		errs = append(errs, "finished_at is required")
	}
	// Optional, but a token that is not a UUID cannot do the one job it has.
	// Two clients whose "same submission" tokens collide would have their
	// records silently merged, and a value that is not a UUID is the most
	// likely way to get there — an empty string, a build number, a filename.
	if e.ClientRecordID != nil {
		if _, err := uuid.Parse(*e.ClientRecordID); err != nil {
			errs = append(errs, fmt.Sprintf("client_record_id %q is invalid, must be a UUID",
				*e.ClientRecordID))
		}
	}

	return errs
}
