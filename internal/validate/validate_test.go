package validate

import (
	"testing"
	"time"

	"github.com/nesono/evidence-store/internal/model"
	"github.com/stretchr/testify/assert"
)

func validEvidence() *model.EvidenceCreate {
	return &model.EvidenceCreate{
		Repo:         "org/repo",
		Branch:       "main",
		RCSRef:       "abc123",
		ProcedureRef: "//pkg:test",
		EvidenceType: "ci",
		Source:       "ci-bot",
		Result:       model.ResultPass,
		FinishedAt:   model.FlexibleTime{Time: time.Now()},
	}
}

func TestEvidenceCreateValid(t *testing.T) {
	errs := EvidenceCreate(validEvidence())
	assert.Empty(t, errs)
}

func TestEvidenceCreateMissingFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(e *model.EvidenceCreate)
		errMsg string
	}{
		{"missing repo", func(e *model.EvidenceCreate) { e.Repo = "" }, "repo is required"},
		{"missing branch", func(e *model.EvidenceCreate) { e.Branch = "" }, "branch is required"},
		{"missing rcs_ref", func(e *model.EvidenceCreate) { e.RCSRef = "" }, "rcs_ref is required"},
		{"missing procedure_ref", func(e *model.EvidenceCreate) { e.ProcedureRef = "" }, "procedure_ref is required"},
		{"missing evidence_type", func(e *model.EvidenceCreate) { e.EvidenceType = "" }, "evidence_type is required"},
		{"missing source", func(e *model.EvidenceCreate) { e.Source = "" }, "source is required"},
		{"missing finished_at", func(e *model.EvidenceCreate) { e.FinishedAt = model.FlexibleTime{} }, "finished_at is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEvidence()
			tt.mutate(e)
			errs := EvidenceCreate(e)
			assert.Contains(t, errs, tt.errMsg)
		})
	}
}

func TestEvidenceCreateAllFieldsMissing(t *testing.T) {
	errs := EvidenceCreate(&model.EvidenceCreate{})
	assert.GreaterOrEqual(t, len(errs), 8, "expected errors for all required fields")
}

func TestEvidenceCreateInvalidResult(t *testing.T) {
	e := validEvidence()
	e.Result = "INVALID"
	errs := EvidenceCreate(e)
	assert.Len(t, errs, 1)
	assert.Contains(t, errs[0], "result")
}

// evidence_type is a closed set of three: how the evidence was collected, which
// is what tells a reader what the metadata means. It used to be any lowercase
// word, and the store filled up with `bazel`, `pytest`, `gotest` and `junit` —
// four spellings of "a machine ran it" that no query could treat as one thing.
func TestEvidenceTypeValidation(t *testing.T) {
	for _, et := range []string{"ci", "manual_test", "demonstration"} {
		e := validEvidence()
		e.EvidenceType = et
		errs := EvidenceCreate(e)
		assert.Empty(t, errs, "expected %q to be valid", et)
	}

	invalid := []string{
		"bazel",       // a runner, not a collection method — now metadata.collector
		"pytest",      // ditto
		"manual",      // the old spelling of manual_test
		"CI",          // the label, not the value
		"Manual Test", // ditto
		"manual-test", // hyphen
		"ci ",         // stray whitespace
		"demonstration2",
		"", // empty (caught as required)
	}
	for _, et := range invalid {
		e := validEvidence()
		e.EvidenceType = et
		errs := EvidenceCreate(e)
		assert.NotEmpty(t, errs, "expected %q to be invalid", et)
	}
}

// The error has to name the three, because a client sending `bazel` cannot
// otherwise tell what it is supposed to send instead.
func TestEvidenceTypeErrorNamesTheAllowedValues(t *testing.T) {
	e := validEvidence()
	e.EvidenceType = "bazel"
	errs := EvidenceCreate(e)
	assert.Len(t, errs, 1)
	for _, want := range []string{"bazel", "ci", "manual_test", "demonstration"} {
		assert.Contains(t, errs[0], want)
	}
}

func TestInheritanceCreateValid(t *testing.T) {
	c := &model.InheritanceCreate{
		Repo:          "org/repo",
		SourceRCSRef:  "abc",
		TargetRCSRef:  "def",
		Justification: "no changes",
		CreatedBy:     "user",
	}
	errs := InheritanceCreate(c)
	assert.Empty(t, errs)
}

func TestInheritanceCreateMissingFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *model.InheritanceCreate)
		errMsg string
	}{
		{"missing repo", func(c *model.InheritanceCreate) { c.Repo = "" }, "repo is required"},
		{"missing source_rcs_ref", func(c *model.InheritanceCreate) { c.SourceRCSRef = "" }, "source_rcs_ref is required"},
		{"missing target_rcs_ref", func(c *model.InheritanceCreate) { c.TargetRCSRef = "" }, "target_rcs_ref is required"},
		{"missing justification", func(c *model.InheritanceCreate) { c.Justification = "" }, "justification is required"},
		{"missing created_by", func(c *model.InheritanceCreate) { c.CreatedBy = "" }, "created_by is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &model.InheritanceCreate{
				Repo: "org/repo", SourceRCSRef: "abc", TargetRCSRef: "def",
				Justification: "reason", CreatedBy: "user",
			}
			tt.mutate(c)
			errs := InheritanceCreate(c)
			assert.Contains(t, errs, tt.errMsg)
		})
	}
}
