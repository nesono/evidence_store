package campaign

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

//go:embed testdata/*.md
var fixtures embed.FS

func TestProcedureFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"simple", []string{"station", "status_visible", "result"}},
		{"measurement", []string{"surface", "distance", "supporting_evidence", "assessment"}},
		{"grouped", []string{"operator", "display_ready", "display_ready_comment", "display_ready_issues", "recording_ready", "recording_ready_comment"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := fixtures.ReadFile("testdata/" + tc.name + ".md")
			if err != nil {
				t.Fatal(err)
			}
			p, err := ParseProcedure(string(data))
			if err != nil {
				t.Fatal(err)
			}
			if p.Source != string(data) || p.SchemaVersion != 1 {
				t.Fatal("source bytes or version lost")
			}
			var ids []string
			for _, b := range p.Blocks {
				switch b.Kind {
				case "field":
					if b.Field == nil || b.Check != nil || b.Markdown != "" {
						t.Fatalf("invalid field block: %+v", b)
					}
					ids = append(ids, b.Field.ID)
					if b.Field.Bind != "" && len(b.Field.Default) != 0 {
						t.Fatal("bound result has an automatic default")
					}
				case "check":
					if b.Check == nil || b.Field != nil || b.Markdown != "" {
						t.Fatalf("invalid check block: %+v", b)
					}
					ids = append(ids, b.Check.ID)
				case "markdown":
					if b.Field != nil || b.Check != nil || b.Markdown == "" {
						t.Fatalf("invalid Markdown block: %+v", b)
					}
				default:
					t.Fatalf("unknown block kind %q", b.Kind)
				}
			}
			if !reflect.DeepEqual(ids, tc.ids) {
				t.Fatalf("control order: got %v, want %v", ids, tc.ids)
			}
			encoded, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			var roundTrip Procedure
			if err := json.Unmarshal(encoded, &roundTrip); err != nil || !reflect.DeepEqual(p, &roundTrip) {
				t.Fatalf("JSON round trip failed: %v", err)
			}
		})
	}
}

func declaration(body string) string {
	return "```test-field\n" + body + "\n```\n"
}

func TestFieldDefaults(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{"type: text\ndefault: ''", `""`},
		{"type: text\ndefault: 'no'", `"no"`},
		{"type: choice\noptions: [dry, wet]\ndefault: wet", `"wet"`},
		{"type: measurement\nunit: m\ndefault: 0", `0`},
		{"type: measurement\nunit: m\ndefault: -1.5", `-1.5`},
		{"type: measurement\nunit: m\ndefault: 1e3", `1000`},
		{"type: text", ""},
	} {
		t.Run(tc.body, func(t *testing.T) {
			p, err := ParseProcedure(declaration("id: observation\nlabel: Observation\n" + tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(p.Blocks[0].Field.Default); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if p.Blocks[0].Field.Required {
				t.Fatal("ordinary fields should not be required by default")
			}
		})
	}
}

func TestInvalidDeclarations(t *testing.T) {
	base := "id: observation\nlabel: Observation\n"
	for _, tc := range []struct{ name, body, want string }{
		{"missing id", "label: Label\ntype: text", "id is required"},
		{"invalid id", base + "type: text\nid: Bad-ID", "duplicate YAML key"},
		{"id syntax", "id: bad-id\nlabel: Label\ntype: text", "id must match"},
		{"missing label", "id: observation\ntype: text", "label is required"},
		{"empty label", "id: observation\nlabel: '  '\ntype: text", "label must be"},
		{"numeric label", "id: observation\nlabel: 42\ntype: text", "label must be"},
		{"missing type", base, "type is required"},
		{"unknown type", base + "type: script", "unsupported field type"},
		{"unknown property", base + "type: text\nrequried: true", "unknown test-field property"},
		{"null", base + "type: text\ndefault: null", "default must be a string"},
		{"numeric text", base + "type: text\ndefault: 123", "default must be a string"},
		{"string boolean", base + "type: text\nrequired: 'true'", "required must be a boolean"},
		{"yaml11 boolean", base + "type: text\nrequired: yes", "required must be a boolean"},
		{"missing unit", base + "type: measurement", "unit is required"},
		{"nonfinite", base + "type: measurement\nunit: m\ndefault: .inf", "finite number"},
		{"nan", base + "type: measurement\nunit: m\ndefault: .nan", "finite number"},
		{"number string", base + "type: measurement\nunit: m\ndefault: '1.2'", "finite number"},
		{"missing options", base + "type: choice", "choice options"},
		{"empty options", base + "type: choice\noptions: []", "choice options"},
		{"duplicate options", base + "type: choice\noptions: [a, a]", "unique strings"},
		{"numeric option", base + "type: choice\noptions: [1, 2]", "unique strings"},
		{"boolean option", base + "type: choice\noptions: [true, false]", "unique strings"},
		{"blank option", base + "type: choice\noptions: ['']", "unique strings"},
		{"unknown default", base + "type: choice\noptions: [a, b]\ndefault: c", "must match an option"},
		{"wrong unit", base + "type: text\nunit: m", "unit is only allowed"},
		{"wrong multiline", base + "type: measurement\nunit: m\nmultiline: false", "multiline is only allowed"},
		{"wrong options", base + "type: text\noptions: []", "options is only allowed"},
		{"wrong bind", base + "type: text\nbind: execution.result", "bind is only allowed"},
		{"unknown binding", base + "type: choice\noptions: [a]\nbind: execution.source", "binding requires"},
		{"result vocabulary", base + "type: choice\noptions: [PASS, FAIL, ERROR, PARTIAL]\nbind: execution.result", "binding requires"},
		{"result default", base + "type: choice\noptions: [PASS, FAIL, ERROR, SKIPPED]\nbind: execution.result\ndefault: PASS", "defaults are not allowed"},
		{"evidence default", base + "type: evidence\ndefault: []", "defaults are not allowed"},
		{"anchor", base + "type: text\ndefault: &a hello", "aliases, anchors"},
		{"alias", base + "type: text\ndefault: *a", "invalid declaration YAML"},
		{"custom tag", base + "type: text\ndefault: !custom hello", "explicit tags"},
		{"explicit tag", base + "type: text\ndefault: !!str hello", "explicit tags"},
		{"merge", base + "type: text\n<<: {required: true}", "merge keys"},
		{"numeric key", base + "type: text\n1: hello", "keys must be strings"},
		{"multiple docs", base + "type: text\n---\nid: other", "exactly one YAML document"},
		{"sequence", "- id: observation", "YAML mapping"},
		{"malformed", "[", "invalid declaration YAML"},
		{"empty", "", "invalid declaration YAML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseProcedure(declaration(tc.body))
			if p != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got procedure=%v, error=%v; want %q", p, err, tc.want)
			}
		})
	}
}

func TestChecksAndIdentity(t *testing.T) {
	check := "```test-check\nid: ready\nlabel: Ready\nbehavior: expect\n```\n"
	for _, tc := range []struct{ name, source, want string }{
		{"assert", strings.Replace(check, "expect", "assert", 1), "only check behavior expect"},
		{"check default", strings.Replace(check, "behavior: expect", "behavior: expect\ndefault: PASS", 1), "unknown test-check property"},
		{"missing behavior", strings.Replace(check, "behavior: expect\n", "", 1), "behavior is required"},
		{"duplicate check", check + check, "duplicate control id"},
		{"shared namespace", check + declaration("id: ready\nlabel: Ready\ntype: text"), "duplicate control id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProcedure(tc.source)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	result := declaration("id: result\nlabel: Result\ntype: choice\nbind: execution.result\noptions: [SKIPPED, ERROR, FAIL, PASS]")
	if _, err := ParseProcedure(result); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseProcedure(result + strings.Replace(result, "id: result", "id: second_result", 1)); err == nil || !strings.Contains(err.Error(), "bound only once") {
		t.Fatalf("multiple result bindings: %v", err)
	}
	other := strings.ReplaceAll(check, "ready", "stopped")
	first, _ := ParseProcedure(check + other)
	second, _ := ParseProcedure(other + check)
	if !reflect.DeepEqual(first.Blocks[0].Check, second.Blocks[1].Check) {
		t.Fatal("reordering changed the check identity")
	}
}

func TestMarkdownPreservation(t *testing.T) {
	for _, source := range []string{
		"# Plain procedure\n\nObserve the system.\n",
		"````markdown\n```test-check\nnot a real declaration\n```\n````\n",
		"~~~yaml\n```test-field\nnot a real declaration\n```\n~~~\n",
		"```python\nprint('test-field')\n```\n",
		"```unknown\nordinary code with no closing fence",
		"<script>alert('data, not rendered HTML')</script>\n![image](assets/setup.png)\n",
	} {
		p, err := ParseProcedure(source)
		if err != nil || len(p.Blocks) != 1 || p.Blocks[0].Markdown != source {
			t.Fatalf("source %q not preserved: %+v, %v", source, p, err)
		}
	}
	source := "# Heading\r\n\r\n```test-field\r\nid: note\r\ntype: text\r\nlabel: Note\r\n```\r\nAfter\r\n"
	p, err := ParseProcedure(source)
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != source || len(p.Blocks) != 3 || p.Blocks[0].Markdown != "# Heading\r\n\r\n" || p.Blocks[1].Line != 3 || p.Blocks[2].Line != 8 || p.Blocks[2].Markdown != "After\r\n" {
		t.Fatalf("CRLF or line numbers lost: %+v", p)
	}
}

func TestInvalidFencesAndLimits(t *testing.T) {
	for _, source := range []string{
		"```test-script\nanything\n```\n",
		" ```test-field\nid: note\n```\n",
		"~~~test-field\nid: note\n~~~\n",
		"````test-field\nid: note\n````\n",
		"```test-field extra\nid: note\n```\n",
		"```test-field\nid: note\n",
		"", " \n", "invalid\x00", "invalid\xff",
		strings.Repeat("a", MaxProcedureBytes+1),
		declaration("id: note\nlabel: " + strings.Repeat("a", maxDeclarationBytes) + "\ntype: text"),
	} {
		if p, err := ParseProcedure(source); err == nil || p != nil {
			t.Fatalf("accepted invalid source (length %d)", len(source))
		}
	}
	source := "# Heading\n\n" + declaration("id: note\ntype: text\nlabel: Note\nrequired: 'yes'")
	_, err := ParseProcedure(source)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) || parseErr.Line != 7 {
		t.Fatalf("expected source line 7, got %v", err)
	}
}

func TestControlLimit(t *testing.T) {
	var source strings.Builder
	for i := 0; i < maxControls; i++ {
		fmt.Fprintf(&source, "```test-check\nid: check_%d\nlabel: Check\nbehavior: expect\n```\n", i)
	}
	if _, err := ParseProcedure(source.String()); err != nil {
		t.Fatalf("exactly %d controls should be allowed: %v", maxControls, err)
	}
	source.WriteString("```test-check\nid: one_too_many\nlabel: Check\nbehavior: expect\n```\n")
	if _, err := ParseProcedure(source.String()); err == nil || !strings.Contains(err.Error(), "exceeds 1000 controls") {
		t.Fatalf("control limit not enforced: %v", err)
	}
}

func FuzzParseProcedure(f *testing.F) {
	f.Add("# Plain prose\n")
	f.Add(declaration("id: note\ntype: text\nlabel: Note"))
	f.Add("```test-check\nid: ready\nlabel: Ready\nbehavior: expect\n```\n")
	f.Fuzz(func(t *testing.T, source string) {
		p, err := ParseProcedure(source)
		if err != nil {
			if p != nil {
				t.Fatal("partial procedure returned on error")
			}
			return
		}
		if p.Source != source {
			t.Fatal("source changed")
		}
		if _, err := json.Marshal(p); err != nil {
			t.Fatalf("non-JSON procedure: %v", err)
		}
	})
}
