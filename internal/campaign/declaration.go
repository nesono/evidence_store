package campaign

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var controlID = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func parseDeclaration(kind, body string, firstLine int) (Block, error) {
	fields, err := decodeDeclaration(kind, body, firstLine)
	if err != nil {
		return Block{}, err
	}
	id, label, err := declarationIdentity(fields, firstLine)
	if err != nil {
		return Block{}, err
	}
	if kind == "test-check" {
		return parseCheck(fields, id, label, firstLine)
	}
	return parseField(fields, id, label, firstLine)
}

// Decode YAML through nodes so YAML's permissive scalar-to-string conversions
// cannot turn, for example, a numeric choice option into a different API type.
func decodeDeclaration(kind, body string, firstLine int) (map[string]*yaml.Node, error) {
	decoder := yaml.NewDecoder(strings.NewReader(body))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, errorAt(firstLine, "invalid declaration YAML: %v", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errorAt(firstLine, "declaration must contain exactly one YAML document")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errorAt(firstLine, "declaration must be a YAML mapping")
	}
	root := doc.Content[0]
	if err := validateYAML(root, firstLine, 0); err != nil {
		return nil, err
	}
	fields := make(map[string]*yaml.Node)
	for i := 0; i < len(root.Content); i += 2 {
		fields[root.Content[i].Value] = root.Content[i+1]
	}
	allowed := []string{"id", "label", "behavior"}
	if kind == "test-field" {
		allowed = []string{"id", "type", "label", "required", "multiline", "options", "unit", "bind", "default"}
	}
	for i := 0; i < len(root.Content); i += 2 {
		key := root.Content[i]
		if !slices.Contains(allowed, key.Value) {
			return nil, errorAt(firstLine+key.Line-1, "unknown %s property %q", kind, key.Value)
		}
	}
	return fields, nil
}

func declarationIdentity(fields map[string]*yaml.Node, firstLine int) (string, string, error) {
	id, err := stringProperty(fields, "id", true, firstLine)
	if err != nil {
		return "", "", err
	}
	if !controlID.MatchString(id) {
		return "", "", errorAt(firstLine, "id must match [a-z][a-z0-9_]*")
	}
	label, err := stringProperty(fields, "label", true, firstLine)
	return id, label, err
}

func parseCheck(fields map[string]*yaml.Node, id, label string, firstLine int) (Block, error) {
	behavior, err := stringProperty(fields, "behavior", true, firstLine)
	if err != nil {
		return Block{}, err
	}
	if behavior != "expect" {
		return Block{}, errorAt(firstLine, "only check behavior expect is supported")
	}
	return Block{Kind: "check", Check: &Check{ID: id, Label: label, Behavior: behavior}}, nil
}

func parseField(fields map[string]*yaml.Node, id, label string, firstLine int) (Block, error) {
	f := &Field{ID: id, Label: label}
	var err error
	f.Type, err = stringProperty(fields, "type", true, firstLine)
	if err != nil {
		return Block{}, err
	}
	if !slices.Contains([]string{"text", "choice", "measurement", "evidence"}, f.Type) {
		return Block{}, errorAt(firstLine, "unsupported field type %q", f.Type)
	}
	f.Required, err = boolProperty(fields, "required", firstLine)
	if err != nil {
		return Block{}, err
	}
	if err := parseFieldConfiguration(f, fields, firstLine); err != nil {
		return Block{}, err
	}
	if err := parseResultBinding(f, fields, firstLine); err != nil {
		return Block{}, err
	}
	if err := parseFieldDefault(f, fields["default"], firstLine); err != nil {
		return Block{}, err
	}
	return Block{Kind: "field", Field: f}, nil
}

func parseFieldConfiguration(f *Field, fields map[string]*yaml.Node, firstLine int) error {
	// Reject properties on the wrong type even when their value is false or
	// empty. Otherwise a misspelled field type can quietly discard author intent.
	for _, property := range []struct{ name, fieldType string }{
		{"multiline", "text"}, {"options", "choice"}, {"unit", "measurement"}, {"bind", "choice"},
	} {
		if fields[property.name] != nil && f.Type != property.fieldType {
			return errorAt(firstLine, "%s is only allowed on %s fields", property.name, property.fieldType)
		}
	}
	var err error
	switch f.Type {
	case "text":
		f.Multiline, err = boolProperty(fields, "multiline", firstLine)
	case "measurement":
		f.Unit, err = stringProperty(fields, "unit", true, firstLine)
	case "choice":
		f.Options, err = parseChoiceOptions(fields["options"], firstLine)
	}
	return err
}

func parseChoiceOptions(options *yaml.Node, firstLine int) ([]string, error) {
	if options == nil || options.Kind != yaml.SequenceNode || len(options.Content) == 0 {
		return nil, errorAt(firstLine, "choice options must be a nonempty list of unique strings")
	}
	var values []string
	for _, option := range options.Content {
		if option.Tag != "!!str" || strings.TrimSpace(option.Value) == "" || slices.Contains(values, option.Value) {
			return nil, errorAt(firstLine+option.Line-1, "choice options must be nonempty, unique strings")
		}
		values = append(values, option.Value)
	}
	return values, nil
}

func parseResultBinding(f *Field, fields map[string]*yaml.Node, firstLine int) error {
	if fields["bind"] == nil {
		return nil
	}
	var err error
	f.Bind, err = stringProperty(fields, "bind", true, firstLine)
	if err != nil {
		return err
	}
	if f.Bind != "execution.result" || len(f.Options) != 4 {
		return errorAt(firstLine, "binding requires execution.result and exactly PASS, FAIL, ERROR, SKIPPED")
	}
	for _, result := range []string{"PASS", "FAIL", "ERROR", "SKIPPED"} {
		if !slices.Contains(f.Options, result) {
			return errorAt(firstLine, "binding requires execution.result and exactly PASS, FAIL, ERROR, SKIPPED")
		}
	}
	return nil
}

func parseFieldDefault(f *Field, def *yaml.Node, firstLine int) error {
	if def == nil {
		return nil
	}
	if f.Bind != "" || f.Type == "evidence" {
		return errorAt(firstLine+def.Line-1, "defaults are not allowed on bound result or evidence fields")
	}
	value, err := defaultValue(f, def)
	if err != nil {
		return errorAt(firstLine+def.Line-1, "%v", err)
	}
	f.Default, err = json.Marshal(value)
	if err != nil {
		return errorAt(firstLine+def.Line-1, "invalid default: %v", err)
	}
	return nil
}

func defaultValue(field *Field, node *yaml.Node) (any, error) {
	switch field.Type {
	case "text", "choice":
		if node.Tag != "!!str" {
			return nil, fmt.Errorf("%s default must be a string", field.Type)
		}
		if field.Type == "choice" && !slices.Contains(field.Options, node.Value) {
			return nil, fmt.Errorf("choice default must match an option")
		}
		return node.Value, nil
	case "measurement":
		var value float64
		if (node.Tag != "!!int" && node.Tag != "!!float") || node.Decode(&value) != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, fmt.Errorf("measurement default must be a finite number")
		}
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported default type %q", field.Type)
	}
}

func stringProperty(fields map[string]*yaml.Node, key string, required bool, firstLine int) (string, error) {
	node := fields[key]
	if node == nil {
		if required {
			return "", errorAt(firstLine, "%s is required", key)
		}
		return "", nil
	}
	if node.Tag != "!!str" || strings.TrimSpace(node.Value) == "" {
		return "", errorAt(firstLine+node.Line-1, "%s must be a nonempty string", key)
	}
	return node.Value, nil
}

func boolProperty(fields map[string]*yaml.Node, key string, firstLine int) (bool, error) {
	node := fields[key]
	if node == nil {
		return false, nil
	}
	if node.Tag != "!!bool" {
		return false, errorAt(firstLine+node.Line-1, "%s must be a boolean", key)
	}
	var value bool
	if err := node.Decode(&value); err != nil {
		return false, errorAt(firstLine+node.Line-1, "invalid %s: %v", key, err)
	}
	return value, nil
}

func validateYAML(node *yaml.Node, firstLine, depth int) error {
	line := firstLine + node.Line - 1
	if depth > 20 {
		return errorAt(line, "YAML nesting exceeds 20 levels")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Style&yaml.TaggedStyle != 0 {
		return errorAt(line, "YAML aliases, anchors, and explicit tags are not supported")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Tag != "!!str" || key.Value == "<<" {
				return errorAt(firstLine+key.Line-1, "YAML keys must be strings; merge keys are not supported")
			}
			if seen[key.Value] {
				return errorAt(firstLine+key.Line-1, "duplicate YAML key %q", key.Value)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateYAML(child, firstLine, depth+1); err != nil {
			return err
		}
	}
	return nil
}
