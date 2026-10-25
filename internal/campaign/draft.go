package campaign

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"slices"
	"strings"

	"github.com/nesono/evidence-store/internal/model"
)

// DraftData is unfinished work, not evidence. Required acquisition fields and
// checks may be absent. Result is an explicit provisional assessment only.
type DraftData struct {
	Repo    string                     `json:"repo"`
	Branch  string                     `json:"branch"`
	RCSRef  string                     `json:"rcs_ref"`
	Notes   string                     `json:"notes"`
	Result  string                     `json:"result"`
	Answers map[string]json.RawMessage `json:"answers"`
}

// InitialDraft copies input defaults, never check outcomes or an overall result.
func InitialDraft(p *Procedure) DraftData {
	d := DraftData{Answers: map[string]json.RawMessage{}}
	for _, block := range p.Blocks {
		if f := block.Field; f != nil && f.Bind == "" && len(f.Default) != 0 {
			d.Answers[f.ID] = bytes.Clone(f.Default)
		}
	}
	return d
}

func ValidateDraft(p *Procedure, d DraftData) error {
	if d.Answers == nil {
		return fmt.Errorf("answers must be an object")
	}
	if d.Result != "" && !model.EvidenceResult(d.Result).Valid() {
		return fmt.Errorf("result must be empty, PASS, FAIL, ERROR, or SKIPPED")
	}
	controls := make(map[string]Block)
	for _, b := range p.Blocks {
		if b.Field != nil {
			controls[b.Field.ID] = b
		}
		if b.Check != nil {
			controls[b.Check.ID] = b
		}
	}
	for id, raw := range d.Answers {
		b, ok := controls[id]
		if !ok {
			return fmt.Errorf("unknown answer field %q", id)
		}
		if err := validateAnswer(b, raw); err != nil {
			return fmt.Errorf("answer %q: %w", id, err)
		}
	}
	return nil
}

func validateAnswer(b Block, raw json.RawMessage) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("omit unanswered fields instead of sending null")
	}
	if b.Check != nil {
		return validateCheckAnswer(raw)
	}
	f := b.Field
	if f.Bind != "" {
		return fmt.Errorf("bound result belongs in result, not answers")
	}
	switch f.Type {
	case "text", "choice":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fmt.Errorf("expected a string")
		}
		if f.Type == "choice" && !slices.Contains(f.Options, value) {
			return fmt.Errorf("value is not a declared option")
		}
	case "measurement":
		var value float64
		if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("expected a finite number in %s", f.Unit)
		}
	case "evidence":
		return validateDraftLinks(raw)
	default:
		return fmt.Errorf("unsupported field type %q", f.Type)
	}
	return nil
}

func validateCheckAnswer(raw json.RawMessage) error {
	var answer struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason,omitempty"`
	}
	if err := StrictJSON(raw, &answer); err != nil {
		return err
	}
	if !slices.Contains([]string{"PASS", "FAIL", "NOT_EXECUTED"}, answer.Outcome) {
		return fmt.Errorf("expected PASS, FAIL, or NOT_EXECUTED")
	}
	if answer.Outcome == "NOT_EXECUTED" && strings.TrimSpace(answer.Reason) == "" {
		return fmt.Errorf("NOT_EXECUTED needs a reason")
	}
	return nil
}

// Draft evidence inputs support external links first. CAS attachments require
// draft-owned reachability roots and will be added with the upload workflow.
func validateDraftLinks(raw json.RawMessage) error {
	var links []struct {
		URI   string `json:"uri"`
		Label string `json:"label"`
	}
	if err := StrictJSON(raw, &links); err != nil {
		return err
	}
	for _, link := range links {
		u, err := url.Parse(link.URI)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			return fmt.Errorf("draft evidence must be external HTTPS links without credentials")
		}
	}
	return nil
}

// StrictJSON rejects unknown fields and trailing documents in draft payloads.
func StrictJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
