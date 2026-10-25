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

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		input  string
		status int
	}{
		{"stdin", []string{"-"}, "# Procedure\n", 0},
		{"help", []string{"--help"}, "", 0},
		{"no argument", nil, "", 2},
		{"extra argument", []string{"-", "extra"}, "", 2},
		{"missing file", []string{"/does-not-exist/procedure.md"}, "", 1},
		{"invalid control", []string{"-"}, "```test-script\nanything\n```\n", 1},
		{"oversize", []string{"-"}, strings.Repeat("a", campaign.MaxProcedureBytes+1), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			status := run(tc.args, strings.NewReader(tc.input), &out, &stderr)
			if status != tc.status {
				t.Fatalf("status %d, want %d; %s", status, tc.status, stderr.String())
			}
			if status != 0 && (out.Len() != 0 || stderr.Len() == 0) {
				t.Fatal("failure must produce a diagnostic and no partial JSON")
			}
			if tc.name == "stdin" {
				var p campaign.Procedure
				if err := json.Unmarshal(out.Bytes(), &p); err != nil || p.Source != tc.input {
					t.Fatalf("invalid output: %s, %v", out.String(), err)
				}
			}
		})
	}
}

type brokenIO struct{}

func (brokenIO) Read([]byte) (int, error)  { return 0, errors.New("read failed") }
func (brokenIO) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestIOErrors(t *testing.T) {
	var out, stderr bytes.Buffer
	if status := run([]string{"--help"}, strings.NewReader(""), brokenIO{}, &stderr); status != 1 || !strings.Contains(stderr.String(), "write help") {
		t.Fatalf("help write failure not reported: %d, %s", status, stderr.String())
	}
	stderr.Reset()
	if status := run([]string{"-"}, brokenIO{}, &out, &stderr); status != 1 || !strings.Contains(stderr.String(), "read failed") {
		t.Fatalf("read failure not reported: %d, %s", status, stderr.String())
	}
	stderr.Reset()
	if status := run([]string{"-"}, strings.NewReader("# Procedure"), brokenIO{}, &stderr); status != 1 || !strings.Contains(stderr.String(), "write failed") {
		t.Fatalf("write failure not reported: %d, %s", status, stderr.String())
	}
}

func TestFileInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "procedure.md")
	if err := os.WriteFile(path, []byte("# Procedure\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if status := run([]string{path}, brokenIO{}, &out, &stderr); status != 0 {
		t.Fatalf("file validation failed: %d, %s", status, stderr.String())
	}
	var p campaign.Procedure
	if err := json.Unmarshal(out.Bytes(), &p); err != nil || p.Source != "# Procedure\n" {
		t.Fatalf("file contents not represented: %s, %v", out.String(), err)
	}
}
