// Package campaign validates the portable definitions used by test campaigns.
// Parsing does not render HTML, fetch resources, or execute procedure content.
package campaign

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// MaxProcedureBytes bounds both the CLI input and future importer documents.
	MaxProcedureBytes   = 1 << 20
	maxControls         = 1000
	maxDeclarationBytes = 64 << 10
)

// Procedure retains the author's bytes alongside an ordered representation for
// clients. Answers belong to an execution and never replace these declarations.
type Procedure struct {
	SchemaVersion int     `json:"schema_version"`
	Source        string  `json:"source"`
	Blocks        []Block `json:"blocks"`
}

// Block is either ordinary Markdown or one validated input/check declaration.
// Line is one-based in Source, including for CRLF documents.
type Block struct {
	Kind     string `json:"kind"`
	Line     int    `json:"line"`
	Markdown string `json:"markdown,omitempty"`
	Field    *Field `json:"field,omitempty"`
	Check    *Check `json:"check,omitempty"`
}

type Field struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Label     string          `json:"label"`
	Required  bool            `json:"required"`
	Multiline bool            `json:"multiline,omitempty"`
	Options   []string        `json:"options,omitempty"`
	Unit      string          `json:"unit,omitempty"`
	Bind      string          `json:"bind,omitempty"`
	Default   json.RawMessage `json:"default,omitempty"`
}

type Check struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Behavior string `json:"behavior"`
}

// ParseError identifies the source line rather than just a line inside a YAML
// fence, so an author can fix the original document directly.
type ParseError struct {
	Line    int
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
}

func errorAt(line int, message string, args ...any) error {
	return &ParseError{Line: line, Message: fmt.Sprintf(message, args...)}
}

// ParseProcedure recognizes only top-level ```test-field and ```test-check
// fences. Other fenced code is preserved as prose, including examples enclosed
// by longer fences. Unknown or malformed test-* controls fail closed.
func ParseProcedure(source string) (*Procedure, error) {
	if len(source) > MaxProcedureBytes {
		return nil, errorAt(1, "procedure exceeds %d bytes", MaxProcedureBytes)
	}
	if !utf8.ValidString(source) || strings.ContainsRune(source, '\x00') {
		return nil, errorAt(1, "procedure must be UTF-8 without NUL bytes")
	}
	if strings.TrimSpace(source) == "" {
		return nil, errorAt(1, "procedure is empty")
	}
	p := &Procedure{SchemaVersion: 1, Source: source, Blocks: []Block{}}
	lines := strings.SplitAfter(source, "\n")
	seen := make(map[string]int)
	boundResult := false
	proseStart := 0
	var ordinary fence
	flush := func(end int) {
		text := strings.Join(lines[proseStart:end], "")
		if text != "" {
			p.Blocks = append(p.Blocks, Block{Kind: "markdown", Line: proseStart + 1, Markdown: text})
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lineText(lines[i])
		if ordinary.width > 0 {
			if ordinary.closes(line) {
				ordinary = fence{}
			}
			continue
		}
		f, ok := openingFence(line)
		if !ok {
			continue
		}
		if !strings.HasPrefix(f.info, "test-") {
			ordinary = f
			continue
		}
		if line != "```test-field" && line != "```test-check" {
			return nil, errorAt(i+1, "expected a top-level ```test-field or ```test-check fence")
		}
		start := i
		for i++; i < len(lines) && lineText(lines[i]) != "```"; i++ {
		}
		if i == len(lines) {
			return nil, errorAt(start+1, "unclosed %s declaration", f.info)
		}
		body := strings.Join(lines[start+1:i], "")
		if len(body) > maxDeclarationBytes {
			return nil, errorAt(start+1, "declaration exceeds %d bytes", maxDeclarationBytes)
		}
		block, err := parseDeclaration(f.info, body, start+2)
		if err != nil {
			return nil, err
		}
		block.Line = start + 1
		id := ""
		if block.Field != nil {
			id = block.Field.ID
			if block.Field.Bind != "" {
				if boundResult {
					return nil, errorAt(start+1, "execution.result may be bound only once")
				}
				boundResult = true
			}
		} else {
			id = block.Check.ID
		}
		if prior, exists := seen[id]; exists {
			return nil, errorAt(start+1, "duplicate control id %q (first declared at line %d)", id, prior)
		}
		seen[id] = start + 1
		if len(seen) > maxControls {
			return nil, errorAt(start+1, "procedure exceeds %d controls", maxControls)
		}
		flush(start)
		p.Blocks = append(p.Blocks, block)
		proseStart = i + 1
	}
	flush(len(lines))
	return p, nil
}

func lineText(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

type fence struct {
	marker byte
	width  int
	info   string
}

func openingFence(line string) (fence, bool) {
	text := strings.TrimLeft(line, " \t")
	if len(text) < 3 || (text[0] != '`' && text[0] != '~') {
		return fence{}, false
	}
	i := 1
	for i < len(text) && text[i] == text[0] {
		i++
	}
	if i < 3 {
		return fence{}, false
	}
	return fence{marker: text[0], width: i, info: strings.TrimSpace(text[i:])}, true
}

func (f fence) closes(line string) bool {
	other, ok := openingFence(line)
	return ok && other.marker == f.marker && other.width >= f.width && other.info == ""
}
