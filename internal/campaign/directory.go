package campaign

import (
	"fmt"
	"os"
	"path"
)

// LoadPackageDirectory reads only manifest-selected definitions, descriptions,
// and explicitly declared assets. Unrelated source files are never packaged.
func LoadPackageDirectory(directory string) (map[string][]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }() // Read-only directory handle.
	loader := directoryLoader{root: root, files: map[string][]byte{}}
	data, err := loader.read("manifest.yaml")
	if err != nil {
		return nil, err
	}
	m, err := parseManifest(data)
	if err != nil {
		return nil, err
	}
	for _, entry := range m.Scenarios {
		if err := loader.scenario(entry); err != nil {
			return nil, err
		}
	}
	return loader.files, nil
}

type directoryLoader struct {
	root  *os.Root
	files map[string][]byte
	total int64
}

func (l *directoryLoader) scenario(entry ScenarioEntry) error {
	data, err := l.read(entry.Path)
	if err != nil {
		return err
	}
	s, err := parseScenario(data, entry)
	if err != nil {
		return err
	}
	description, err := relativeFile(entry.Path, s.Description)
	if err != nil {
		return err
	}
	if _, err := l.read(description); err != nil {
		return err
	}
	for _, asset := range s.Assets {
		name, err := relativeFile(description, asset)
		if err != nil {
			return err
		}
		if _, err := l.read(name); err != nil {
			return err
		}
	}
	return nil
}

func (l *directoryLoader) read(name string) ([]byte, error) {
	if data, exists := l.files[name]; exists {
		return data, nil
	}
	if !archivePath(name) || len(l.files) >= MaxPackageEntries {
		return nil, fmt.Errorf("invalid path or too many package files: %q", name)
	}
	if err := l.regularFile(name); err != nil {
		return nil, err
	}
	f, err := l.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }() // Read-only input.
	data, err := readBounded(f, MaxPackageFileBytes)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	l.total += int64(len(data))
	if l.total > MaxExpandedBytes {
		return nil, fmt.Errorf("package exceeds %d bytes", MaxExpandedBytes)
	}
	l.files[name] = data
	return data, nil
}

func (l *directoryLoader) regularFile(name string) error {
	for current := name; current != "."; current = path.Dir(current) {
		info, err := l.root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: symlinks are not package inputs", current)
		}
		if current == name && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: expected regular file", name)
		}
	}
	return nil
}
