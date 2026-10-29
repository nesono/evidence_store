package campaign

import (
	"encoding/json"
	"testing"
)

func TestDraftValidation(t *testing.T) {
	p := &Procedure{Blocks: []Block{
		{Field: &Field{ID: "text", Type: "text", Required: true, Default: json.RawMessage(`"seed"`)}},
		{Field: &Field{ID: "choice", Type: "choice", Options: []string{"dry", "wet"}}},
		{Field: &Field{ID: "measurement", Type: "measurement", Unit: "m"}},
		{Field: &Field{ID: "evidence", Type: "evidence"}},
		{Field: &Field{ID: "result", Type: "choice", Bind: "execution.result"}},
		{Check: &Check{ID: "check"}},
	}}
	initial := InitialDraft(p)
	if initial.Result != "" || len(initial.Answers) != 1 || string(initial.Answers["text"]) != `"seed"` {
		t.Fatal("unsafe initial defaults")
	}
	if err := ValidateDraft(p, DraftData{Answers: map[string]json.RawMessage{}}); err != nil {
		t.Fatal("missing required inputs must be saveable in drafts", err)
	}
	for _, tc := range []struct {
		id, raw string
		valid   bool
	}{
		{"text", `"observation"`, true}, {"text", `12`, false}, {"text", `null`, false},
		{"choice", `"dry"`, true}, {"choice", `"unknown"`, false},
		{"measurement", `0`, true}, {"measurement", `"1"`, false}, {"measurement", `1e999`, false},
		{"check", `{"outcome":"PASS"}`, true}, {"check", `{"outcome":"NOT_EXECUTED","reason":"equipment unavailable"}`, true},
		{"check", `{"outcome":"NOT_EXECUTED"}`, false}, {"check", `{"outcome":"PARTIAL"}`, false},
		{"check", `{"outcome":"PASS","extra":true}`, false},
		{"evidence", `[{"uri":"https://example.org/log","label":"Log"}]`, true},
		{"evidence", `[{"uri":"/api/v1/blobs/sha256:abc","label":"Log"}]`, false},
		{"evidence", `[{"uri":"https://user:secret@example.org/log","label":"Log"}]`, false},
		{"evidence", `{"uri":"https://example.org"}`, false},
		{"result", `"PASS"`, false}, {"unknown", `"value"`, false},
	} {
		t.Run(tc.id+tc.raw, func(t *testing.T) {
			d := DraftData{Answers: map[string]json.RawMessage{tc.id: json.RawMessage(tc.raw)}}
			err := ValidateDraft(p, d)
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %v, want valid=%v", err, tc.valid)
			}
		})
	}
	if err := ValidateDraft(p, DraftData{Answers: map[string]json.RawMessage{}, Result: "PARTIAL"}); err == nil {
		t.Fatal("invalid overall result")
	}
}

func TestEmptyMetadataRemainsAnExplicitOverride(t *testing.T) {
	d := DraftData{Metadata: map[string]json.RawMessage{}}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var restored DraftData
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Metadata == nil {
		t.Fatal("empty metadata must not disappear and restore package defaults")
	}
}
