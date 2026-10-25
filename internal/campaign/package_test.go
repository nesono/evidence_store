package campaign

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/package
var packageFixtures embed.FS

func fixtureFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	err := fs.WalkDir(packageFixtures, "testdata/package", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := packageFixtures.ReadFile(name)
		files[strings.TrimPrefix(name, "testdata/package/")] = data
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestPackageRoundTrip(t *testing.T) {
	files := fixtureFiles(t)
	data, built, err := BuildPackage(files)
	if err != nil {
		t.Fatal(err)
	}
	p, err := ReadPackage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, built) || p.Digest != contentDigest(data) {
		t.Fatal("package changed on round trip")
	}
	if p.Scenarios[0].Definition.ID != "display" || p.Scenarios[1].Definition.ID != "recording" {
		t.Fatal("manifest scenario order lost")
	}
	if p.Scenarios[0].Procedure.Blocks[1].Check.ID != "status_visible" {
		t.Fatal("procedure parser not integrated")
	}
	// Map iteration order must never affect the archive bytes.
	reordered := map[string][]byte{}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for i := len(names) - 1; i >= 0; i-- {
		reordered[names[i]] = files[names[i]]
	}
	again, _, err := BuildPackage(reordered)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatalf("nondeterministic archive: %v", err)
	}
	reordered[names[0]] = append(bytes.Clone(reordered[names[0]]), '\n')
	_, changed, err := BuildPackage(reordered)
	if err != nil || changed.Digest == p.Digest {
		t.Fatalf("byte change must change digest: %v", err)
	}
	encoded, err := json.Marshal(p)
	if err != nil || bytes.Contains(encoded, []byte("revision")) {
		t.Fatalf("package must not carry a product revision: %v", err)
	}
}

func TestDeterministicHeaders(t *testing.T) {
	data, _, err := BuildPackage(fixtureFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }() // Read-only gzip reader.
	if !z.ModTime.IsZero() || z.Name != "" || z.Comment != "" || z.OS != 255 {
		t.Fatalf("unstable gzip header: %+v", z.Header)
	}
	r := tar.NewReader(z)
	prior := ""
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name <= prior || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" || h.Mode != 0644 || h.ModTime.Unix() != 0 || h.Format != tar.FormatUSTAR {
			t.Fatalf("unstable tar header: %+v", h)
		}
		prior = h.Name
	}
}

func TestInvalidPackageDefinitions(t *testing.T) {
	const scenario = "scenarios/display/scenario.yaml"
	const procedure = "scenarios/display/procedure.md"
	for _, tc := range []struct{ name, file, old, replacement, want string }{
		{"version", "manifest.yaml", "format_version: 1", "format_version: 99", "unsupported package"},
		{"revision", "manifest.yaml", "source:", "source:\n  revision: deadbeef", "unknown field"},
		{"unknown", "manifest.yaml", "format_version: 1", "format_version: 1\nother: true", "unknown field"},
		{"duplicate key", "manifest.yaml", "format_version: 1", "format_version: 1\nformat_version: 1", "duplicate YAML key"},
		{"multiple documents", "manifest.yaml", "format_version: 1", "---\nformat_version: 1\n---", "one YAML document"},
		{"unquoted date", "manifest.yaml", "  name: Equipment patrol", "  name: Equipment patrol\n  planned_start: 2026-10-25", "quote dates"},
		{"date range", "manifest.yaml", "  name: Equipment patrol", "  name: Equipment patrol\n  planned_start: '2026-10-25'\n  planned_end: '2026-10-24'", "precedes"},
		{"numeric id", "manifest.yaml", "id: equipment-patrol", "id: 123", "cannot unmarshal"},
		{"duplicate id", "manifest.yaml", "- id: recording", "- id: display", "duplicate scenario id"},
		{"duplicate path", "manifest.yaml", "path: scenarios/recording/scenario.yaml", "path: scenarios/display/scenario.yaml", "duplicate scenario path"},
		{"missing scenario", "manifest.yaml", "path: scenarios/display/scenario.yaml", "path: missing.yaml", "invalid YAML"},
		{"missing name", "manifest.yaml", "name: Equipment patrol", "name: ''", "nonempty name"},
		{"credentials", "manifest.yaml", "https://example.org/team/scenarios", "https://user:secret@example.org/repo", "source.repository"},
		{"missing description", scenario, "description: procedure.md", "description: missing.md", "procedure is empty"},
		{"path escape", scenario, "description: procedure.md", "description: ../../outside.md", "invalid relative file"},
		{"wrong id", scenario, "id: display", "id: wrong", "matching id"},
		{"bad type", scenario, "manual_test", "Manual Test", "evidence_type"},
		{"notes default", scenario, "cluster: Commissioning", "notes: Instructions", "reserved for execution"},
		{"spoof campaign", scenario, "cluster: Commissioning", "campaign: {}", "reserved for execution"},
		{"nonfinite", scenario, "cluster: Commissioning", "value: .nan", "JSON-compatible"},
		{"null", scenario, "cluster: Commissioning", "value: null", "null values"},
		{"alias", scenario, "cluster: Commissioning", "cluster: &cluster Commissioning", "anchors"},
		{"duplicate tag", scenario, "tags: [patrol]", "tags: [patrol, patrol]", "tags must"},
		{"duplicate procedure ref", "scenarios/recording/scenario.yaml", "patrol/recording", "patrol/display", "duplicate procedure_ref"},
		{"numeric tag", scenario, "tags: [patrol]", "tags: [123]", "cannot unmarshal"},
		{"procedure", procedure, "behavior: expect", "behavior: assert", "only check behavior"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := fixtureFiles(t)
			files[tc.file] = []byte(strings.Replace(string(files[tc.file]), tc.old, tc.replacement, 1))
			_, p, err := BuildPackage(files)
			if p != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %q", err, tc.want)
			}
		})
	}
	files := fixtureFiles(t)
	files["secret.txt"] = []byte("unrelated file")
	if _, _, err := BuildPackage(files); err == nil || !strings.Contains(err.Error(), "undeclared") {
		t.Fatalf("undeclared file accepted: %v", err)
	}
}

// Build adversarial archives without going through the package validator.
func rawArchive(t *testing.T, headers []*tar.Header, bodies [][]byte) []byte {
	t.Helper()
	var tarBytes bytes.Buffer
	w := tar.NewWriter(&tarBytes)
	for i, h := range headers {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if len(bodies) > i {
			if _, err := w.Write(bodies[i]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return gzipBytes(t, tarBytes.Bytes())
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	z := gzip.NewWriter(&out)
	if _, err := z.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestArchiveRejections(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../b", "a//b", "a\\b", "C:drive", "a%2fb", "./manifest.yaml"} {
		t.Run(name, func(t *testing.T) {
			data := rawArchive(t, []*tar.Header{{Name: name, Mode: 0644}}, nil)
			if _, err := ReadPackage(bytes.NewReader(data)); err == nil {
				t.Fatal("accepted unsafe path")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar} {
		data := rawArchive(t, []*tar.Header{{Name: "link", Typeflag: kind, Linkname: "manifest.yaml"}}, nil)
		if _, err := ReadPackage(bytes.NewReader(data)); err == nil {
			t.Fatalf("accepted entry type %d", kind)
		}
	}
	for _, headers := range [][]*tar.Header{
		{{Name: "same"}, {Name: "same"}},
		{{Name: "dir/", Typeflag: tar.TypeDir}, {Name: "dir"}},
		{{Name: "parent"}, {Name: "parent/child"}},
		{{Name: "extended", Format: tar.FormatPAX, PAXRecords: map[string]string{"example.key": "value"}}},
	} {
		if _, err := ReadPackage(bytes.NewReader(rawArchive(t, headers, nil))); err == nil {
			t.Fatal("accepted duplicate, conflicting, or extended entry")
		}
	}
	valid, _, err := BuildPackage(fixtureFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)-8] ^= 0xff
	for _, data := range [][]byte{nil, []byte("not gzip"), valid[:len(valid)-1], corrupt, append(bytes.Clone(valid), 1), append(bytes.Clone(valid), valid...)} {
		if _, err := ReadPackage(bytes.NewReader(data)); err == nil {
			t.Fatal("accepted corrupt or trailing gzip data")
		}
	}
	z, err := gzip.NewReader(bytes.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := io.ReadAll(z)
	_ = z.Close() // Read-only test input.
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPackage(bytes.NewReader(gzipBytes(t, append(expanded, []byte("hidden file")...)))); err == nil {
		t.Fatal("accepted nonzero data after tar terminator")
	}
}

func TestPackageLimits(t *testing.T) {
	if _, err := readBounded(strings.NewReader("1234"), 3); err == nil {
		t.Fatal("bounded reader accepted too many bytes")
	}
	if _, err := validateHeader(&tar.Header{Name: "large", Size: MaxPackageFileBytes + 1}, map[string]bool{}); err == nil {
		t.Fatal("accepted oversized entry header")
	}
	seen := map[string]bool{}
	for i := 0; i < MaxPackageEntries; i++ {
		seen[strings.Repeat("a", i+1)] = true
	}
	if _, err := validateHeader(&tar.Header{Name: "extra"}, seen); err == nil {
		t.Fatal("accepted too many entries")
	}
	if _, err := ReadPackage(bytes.NewReader(make([]byte, MaxArchiveBytes+1))); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("compressed limit not enforced: %v", err)
	}
	// A small compressed input must still respect the expanded-byte budget.
	bomb := gzipBytes(t, bytes.Repeat([]byte{0}, MaxExpandedBytes+1))
	if _, err := ReadPackage(bytes.NewReader(bomb)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expanded limit not enforced: %v", err)
	}
}

func TestPackageAssets(t *testing.T) {
	const description = "scenarios/display/procedure.md"
	const definition = "scenarios/display/scenario.yaml"
	const asset = "scenarios/display/assets/setup.gif"
	files := fixtureFiles(t)
	files[definition] = append(files[definition], []byte("assets: [assets/setup.gif]\n")...)
	files[description] = append(files[description], []byte("\n![Setup](assets/setup.gif)\n")...)
	files[asset] = []byte("GIF89a")
	data, p, err := BuildPackage(files)
	if err != nil {
		t.Fatal(err)
	}
	if p.Scenarios[0].Assets[asset] != contentDigest(files[asset]) {
		t.Fatal("missing asset digest")
	}
	if _, err := ReadPackage(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	files[definition] = bytes.Replace(files[definition], []byte("assets: [assets/setup.gif]"), []byte("assets: [assets/setup.gif, assets/setup.gif]"), 1)
	if _, _, err := BuildPackage(files); err == nil {
		t.Fatal("accepted duplicate asset declaration")
	}
	files[definition] = bytes.Replace(files[definition], []byte("assets: [assets/setup.gif, assets/setup.gif]"), []byte("assets: [assets/setup.gif]"), 1)
	for _, target := range []string{"https://example.org/image.gif", "missing.gif", "../setup.gif", "/setup.gif"} {
		files[description] = []byte("![Setup](" + target + ")")
		if _, _, err := BuildPackage(files); err == nil {
			t.Fatalf("accepted undeclared image %q", target)
		}
	}
	files[description] = []byte("```markdown\n![Example](https://example.org/image.gif)\n```\n`![Example](missing.gif)`\n")
	if _, _, err := BuildPackage(files); err != nil {
		t.Fatalf("fenced or inline image examples should be ignored: %v", err)
	}
	delete(files, asset)
	if _, _, err := BuildPackage(files); err == nil {
		t.Fatal("accepted missing asset")
	}
	files[asset] = []byte("<svg>unsupported media</svg>")
	if _, _, err := BuildPackage(files); err == nil {
		t.Fatal("accepted unsupported asset")
	}
}

func TestTraceability(t *testing.T) {
	files := fixtureFiles(t)
	const name = "scenarios/display/scenario.yaml"
	link := "\ntraceability:\n  - kind: requirement\n    id: REQ-DISPLAY\n    repository: https://example.org/specs\n    path: requirements/display.md#REQ-DISPLAY\n    entry_version: '1'\n"
	files[name] = append(files[name], []byte(link)...)
	_, p, err := BuildPackage(files)
	if err != nil || p.Scenarios[0].Definition.Traceability[0].EntryVersion != "1" {
		t.Fatalf("valid traceability lost: %v", err)
	}
	for _, replacement := range []string{
		strings.Replace(link, "kind: requirement", "kind: unknown", 1),
		strings.Replace(link, "requirements/display.md", "../escape.md", 1),
		link + "    revision: deadbeef\n",
	} {
		files = fixtureFiles(t)
		files[name] = append(files[name], []byte(replacement)...)
		if _, _, err := BuildPackage(files); err == nil {
			t.Fatal("invalid traceability accepted")
		}
	}
}

func writeFixture(t *testing.T, directory string) {
	t.Helper()
	for name, data := range fixtureFiles(t) {
		full := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirectoryBuild(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("not an input"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := LoadPackageDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, fixtureFiles(t)) {
		t.Fatal("loaded unrelated files or lost declared inputs")
	}
	first, _, err := BuildPackage(files)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "manifest.yaml")
	if err := os.Chtimes(manifest, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifest, 0600); err != nil {
		t.Fatal(err)
	}
	files, err = LoadPackageDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := BuildPackage(files)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("filesystem metadata affected archive: %v", err)
	}
	if err := os.Rename(manifest, manifest+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("manifest.yaml.real", manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPackageDirectory(dir); err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("accepted symlink input: %v", err)
	}
}
