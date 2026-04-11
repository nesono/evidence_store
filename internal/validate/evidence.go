package validate

import (
	"fmt"
	"regexp"

	"github.com/nesono/evidence-store/internal/model"
)

var evidenceTypeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

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
	} else if !evidenceTypeRe.MatchString(e.EvidenceType) {
		errs = append(errs, fmt.Sprintf("evidence_type %q must match pattern %s", e.EvidenceType, evidenceTypeRe.String()))
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

	return errs
}
