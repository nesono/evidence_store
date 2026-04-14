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
		EvidenceType: "bazel",
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

func TestEvidenceTypeValidation(t *testing.T) {
	valid := []string{"bazel", "manual", "junit_xml", "a", "a1b2c3"}
	for _, et := range valid {
		e := validEvidence()
		e.EvidenceType = et
		errs := EvidenceCreate(e)
		assert.Empty(t, errs, "expected %q to be valid", et)
	}

	invalid := []string{
		"BAZEL",      // uppercase
		"bazel-test", // hyphen
		"1bazel",     // starts with digit
		"bazel test", // space
		"bazel.test", // dot
		"a!b",        // special char
		"",           // empty (caught as required)
	}
	for _, et := range invalid {
		e := validEvidence()
		e.EvidenceType = et
		errs := EvidenceCreate(e)
		assert.NotEmpty(t, errs, "expected %q to be invalid", et)
	}
}

func TestEvidenceTypeTooLong(t *testing.T) {
	e := validEvidence()
	e.EvidenceType = "a" + string(make([]byte, 64)) // 65 chars
	errs := EvidenceCreate(e)
	assert.NotEmpty(t, errs)
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
