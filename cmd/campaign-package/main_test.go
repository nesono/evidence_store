package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nesono/evidence-store/internal/campaign"
)

func TestBuildAndValidate(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"manifest.yaml": "format_version: 2\ncampaign: {id: demo, name: Demo}\nsource: {repository: 'https://example.org/scenarios'}\nscenarios: [{procedure_ref: patrol/check, path: scenario.yaml}]\n",
		"scenario.yaml": "title: Check\nprocedure_ref: patrol/check\ndescription: procedure.md\ndefaults: {evidence_type: manual_test}\n",
		"procedure.md":  "# Check\nObserve the display.\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var archive, diagnostics, output bytes.Buffer
	if status := run([]string{"build", dir}, nil, &archive, &diagnostics); status != 0 {
		t.Fatalf("build failed: %s", diagnostics.String())
	}
	digest := strings.TrimSpace(diagnostics.String())
	archivePath := filepath.Join(t.TempDir(), "campaign.tar.gz")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validate(archivePath, nil, brokenIO{}); err == nil {
		t.Fatal("validation output failure ignored")
	}
	diagnostics.Reset()
	if status := run([]string{"validate", "-"}, &archive, &output, &diagnostics); status != 0 {
		t.Fatalf("validate failed: %s", diagnostics.String())
	}
	var p campaign.Package
	if err := json.Unmarshal(output.Bytes(), &p); err != nil || p.Digest != digest {
		t.Fatalf("invalid package JSON: %v", err)
	}
	if err := build(dir, brokenIO{}, &diagnostics); err == nil {
		t.Fatal("archive output failure ignored")
	}
	if err := build(dir, shortWriter{}, &diagnostics); err == nil {
		t.Fatal("short archive write ignored")
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

type brokenIO struct{}

func (brokenIO) Read([]byte) (int, error)  { return 0, errors.New("read failed") }
func (brokenIO) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestFailures(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown", "-"}, {"build"}, {"build", "/does-not-exist"}, {"validate", "/does-not-exist"}, {"validate", "-"}} {
		var output, diagnostics bytes.Buffer
		if status := run(args, brokenIO{}, &output, &diagnostics); status == 0 || diagnostics.Len() == 0 || output.Len() != 0 {
			t.Fatalf("invalid input accepted: %v", args)
		}
	}
}

func TestExampleCommand(t *testing.T) {
	file := filepath.Join(t.TempDir(), "feature-tour.tar.gz")
	args := []string{"example", "--repository", "https://example.org/product", "--branch", "main", "--commit", strings.Repeat("a", 40), "--output", file}
	var output, diagnostics bytes.Buffer
	if code := run(args, nil, &output, &diagnostics); code != 0 {
		t.Fatal(diagnostics.String())
	}
	if err := validate(file, nil, &output); err != nil {
		t.Fatal(err)
	}
	var p campaign.Package
	if err := json.Unmarshal(output.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Scenarios) != 15 || p.Manifest.Product.Commit != strings.Repeat("a", 40) {
		t.Fatal("missing example cases or commit")
	}
	output.Reset()
	if code := run([]string{"example"}, nil, &output, &diagnostics); code == 0 || output.Len() != 0 {
		t.Fatal("example must require product provenance")
	}
}

func TestExampleTitle(t *testing.T) {
	for _, title := range []string{"Vehicle feature tour", "Release: \"Berlin\" – checks", "  "} {
		t.Run(title, func(t *testing.T) {
			args := []string{"example", "--repository", "https://example.org/product", "--branch", "main", "--commit", strings.Repeat("a", 40)}
			if title != "Vehicle feature tour" {
				args = append(args, "--title", title)
			}
			var output, diagnostics bytes.Buffer
			code := run(args, nil, &output, &diagnostics)
			if strings.TrimSpace(title) == "" {
				if code == 0 || output.Len() != 0 {
					t.Fatal("blank title must fail before writing archive bytes")
				}
				return
			}
			if code != 0 {
				t.Fatal(diagnostics.String())
			}
			p, err := campaign.ReadPackage(&output)
			if err != nil {
				t.Fatal(err)
			}
			if p.Manifest.Campaign.Name != title {
				t.Fatalf("title = %q, want %q", p.Manifest.Campaign.Name, title)
			}
		})
	}
}
