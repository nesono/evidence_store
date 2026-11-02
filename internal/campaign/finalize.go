package campaign

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateFinalization checks the saved draft, not an independently submitted
// result. Incomplete work remains saveable but cannot silently become PASS.
func ValidateFinalization(p *Procedure, d DraftData) error {
	if err := ValidateDraft(p, d); err != nil {
		return err
	}
	if strings.TrimSpace(d.Repo) == "" || strings.TrimSpace(d.Branch) == "" || strings.TrimSpace(d.RCSRef) == "" {
		return fmt.Errorf("actual repository, branch, and deployed revision are required")
	}
	if d.Result == "" {
		return fmt.Errorf("an overall result is required")
	}
	missing := []string{}
	failed := 0
	for _, b := range p.Blocks {
		if b.Check != nil {
			outcome, err := finalCheck(b.Check.ID, d)
			if err != nil {
				return err
			}
			if outcome == "FAIL" {
				failed++
			}
		}
		if f := b.Field; f != nil && f.Bind == "" && f.Required && missingAnswer(d.Answers[f.ID]) {
			missing = append(missing, f.ID)
		}
	}
	if len(missing) > 0 && d.Result == "PASS" {
		return fmt.Errorf("required inputs are missing: %s", strings.Join(missing, ", "))
	}
	needsReason := len(missing) > 0 || d.Result == "ERROR" || d.Result == "SKIPPED" || (d.Result == "FAIL" && failed == 0)
	if needsReason && strings.TrimSpace(d.Notes) == "" {
		return fmt.Errorf("tester notes must explain the assessment and any missing required inputs: %s", strings.Join(missing, ", "))
	}
	return nil
}

func finalCheck(id string, d DraftData) (string, error) {
	raw, exists := d.Answers[id]
	if !exists {
		return "", fmt.Errorf("check %q needs an explicit outcome", id)
	}
	var answer struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", err
	}
	if d.Result == "PASS" && answer.Outcome != "PASS" {
		return "", fmt.Errorf("PASS requires check %q to pass", id)
	}
	if d.Result == "SKIPPED" && answer.Outcome != "NOT_EXECUTED" {
		return "", fmt.Errorf("SKIPPED requires check %q to be NOT_EXECUTED", id)
	}
	return answer.Outcome, nil
}

func missingAnswer(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) == ""
	case []any:
		return len(v) == 0
	default:
		return value == nil // Numeric zero is a measured value, not missing.
	}
}
