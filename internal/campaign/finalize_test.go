package campaign

import (
	"encoding/json"
	"testing"
)

func TestFinalizationValidation(t *testing.T) {
	p := &Procedure{Blocks: []Block{
		{Check: &Check{ID: "check"}}, {Field: &Field{ID: "distance", Type: "measurement", Unit: "m", Required: true}},
	}}
	for _, tc := range []struct {
		name, result, outcome, reason, measurement, notes string
		valid                                             bool
	}{
		{"pass", "PASS", "PASS", "", "0", "", true},
		{"missing result", "", "PASS", "", "0", "", false},
		{"false pass", "PASS", "FAIL", "", "0", "", false},
		{"missing check", "PASS", "", "", "0", "", false},
		{"missing measurement", "PASS", "PASS", "", "", "", false},
		{"failure", "FAIL", "FAIL", "", "0", "", true},
		{"early failure", "FAIL", "NOT_EXECUTED", "stopped", "", "Observed failure before acquisition", true},
		{"unexplained omission", "FAIL", "FAIL", "", "", "", false},
		{"skip", "SKIPPED", "NOT_EXECUTED", "not available", "", "No equipment", true},
		{"skip after execution", "SKIPPED", "PASS", "", "0", "skipped", false},
		{"unexplained error", "ERROR", "NOT_EXECUTED", "setup", "0", "", false},
		{"error", "ERROR", "NOT_EXECUTED", "setup", "", "Setup failed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := DraftData{Repo: "repo", Branch: "main", RCSRef: "actual", Result: tc.result, Notes: tc.notes, Answers: map[string]json.RawMessage{}}
			if tc.outcome != "" {
				d.Answers["check"], _ = json.Marshal(map[string]string{"outcome": tc.outcome, "reason": tc.reason})
			}
			if tc.measurement != "" {
				d.Answers["distance"] = json.RawMessage(tc.measurement)
			}
			if err := ValidateFinalization(p, d); (err == nil) != tc.valid {
				t.Fatalf("got %v, valid=%v", err, tc.valid)
			}
		})
	}
}
