package campaign

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

func validateFiles(files map[string][]byte) (*Package, error) {
	if err := validateFileLimits(files); err != nil {
		return nil, err
	}
	manifest, err := parseManifest(files["manifest.yaml"])
	if err != nil {
		return nil, err
	}
	p := &Package{Manifest: manifest}
	used := map[string]bool{"manifest.yaml": true}
	refs := map[string]bool{}
	for _, entry := range manifest.Scenarios {
		s, err := loadScenario(files, entry, used)
		if err != nil {
			return nil, err
		}
		if refs[s.Definition.ProcedureRef] {
			return nil, fmt.Errorf("duplicate procedure_ref %q", s.Definition.ProcedureRef)
		}
		refs[s.Definition.ProcedureRef] = true
		p.Scenarios = append(p.Scenarios, s)
	}
	for name := range files {
		if !used[name] {
			return nil, fmt.Errorf("undeclared package file %q", name)
		}
	}
	return p, nil
}

func validateFileLimits(files map[string][]byte) error {
	if len(files) > MaxPackageEntries {
		return fmt.Errorf("package exceeds %d files", MaxPackageEntries)
	}
	// Include the tar header and padded body for every entry, plus end markers.
	expanded := int64(1024)
	for name, data := range files {
		if !archivePath(name) || len(data) > MaxPackageFileBytes {
			return fmt.Errorf("invalid path or oversized file %q", name)
		}
		if err := validateAncestors(name, files); err != nil {
			return err
		}
		expanded += 512 + ((int64(len(data)) + 511) / 512 * 512)
		if expanded > MaxExpandedBytes {
			return fmt.Errorf("package exceeds %d expanded bytes", MaxExpandedBytes)
		}
	}
	return nil
}

func loadScenario(files map[string][]byte, entry ScenarioEntry, used map[string]bool) (PackagedScenario, error) {
	var loaded PackagedScenario
	definition, err := parseScenario(files[entry.Path], entry)
	if err != nil {
		return loaded, err
	}
	description, err := relativeFile(entry.Path, definition.Description)
	if err != nil {
		return loaded, err
	}
	procedure, err := ParseProcedure(string(files[description]))
	if err != nil {
		return loaded, fmt.Errorf("%s: %w", description, err)
	}
	assets, err := loadAssets(files, description, definition.Assets)
	if err != nil {
		return loaded, err
	}
	if err := validateImages(procedure, description, definition.Assets); err != nil {
		return loaded, err
	}
	used[entry.Path], used[description] = true, true
	for name := range assets {
		used[name] = true
	}
	return PackagedScenario{Definition: definition, Procedure: procedure, Assets: assets}, nil
}

func loadAssets(files map[string][]byte, description string, declared []string) (map[string]string, error) {
	assets := map[string]string{}
	for _, asset := range declared {
		name, err := relativeFile(description, asset)
		if err != nil {
			return nil, err
		}
		data, exists := files[name]
		if !exists || assets[name] != "" {
			return nil, fmt.Errorf("%s: missing or duplicate asset %q", description, asset)
		}
		if !slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, http.DetectContentType(data)) {
			return nil, fmt.Errorf("%s: setup asset must be PNG, JPEG, GIF, or WebP", name)
		}
		assets[name] = contentDigest(data)
	}
	return assets, nil
}

// Use the inline image subset supported by the existing renderer. Unsupported
// image syntax is rejected instead of claiming that an unchecked embed is ready
// offline. Ordinary fenced code and inline code examples are not live embeds.
var inlineImage = regexp.MustCompile(`^!\[[^\]\n]*\]\(([^)\s]+)\)`)
var inlineCode = regexp.MustCompile("`[^`]+`")

func validateImages(p *Procedure, description string, assets []string) error {
	for _, block := range p.Blocks {
		if block.Kind != "markdown" {
			continue
		}
		if err := validateMarkdownImages(block.Markdown, description, assets); err != nil {
			return err
		}
	}
	return nil
}

func validateMarkdownImages(markdown, description string, assets []string) error {
	var code fence
	for _, line := range strings.Split(markdown, "\n") {
		if code.width > 0 {
			if code.closes(line) {
				code = fence{}
			}
			continue
		}
		if f, ok := openingFence(line); ok {
			code = f
			continue
		}
		if err := validateImageLine(inlineCode.ReplaceAllString(line, ""), description, assets); err != nil {
			return err
		}
	}
	return nil
}

func validateImageLine(line, description string, assets []string) error {
	for {
		start := strings.Index(line, "![")
		if start < 0 {
			return nil
		}
		match := inlineImage.FindStringSubmatch(line[start:])
		if match == nil {
			return fmt.Errorf("%s: use inline images ![label](relative/path) and declare them in assets", description)
		}
		if !slices.Contains(assets, match[1]) {
			return fmt.Errorf("%s: image %q must reference a declared package-local asset", description, match[1])
		}
		line = line[start+len(match[0]):]
	}
}
