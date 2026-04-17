package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Snapshot tracks the state of uploaded test.xml files so we only upload new results.
type Snapshot struct {
	// UploadedAt is when the last upload cycle completed.
	UploadedAt time.Time `json:"uploaded_at"`
	// Files maps test.xml absolute paths to their mtime at time of upload.
	Files map[string]time.Time `json:"files"`
}

const snapshotFile = "last-upload.json"

// LoadSnapshot reads the snapshot from .evidence/last-upload.json.
// Returns an empty snapshot if the file doesn't exist.
func LoadSnapshot(workspaceDir string) (*Snapshot, error) {
	path := filepath.Join(EvidenceDir(workspaceDir), snapshotFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Snapshot{Files: make(map[string]time.Time)}, nil
		}
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		// Corrupted file — start fresh.
		return &Snapshot{Files: make(map[string]time.Time)}, nil
	}
	if snap.Files == nil {
		snap.Files = make(map[string]time.Time)
	}
	return &snap, nil
}

// Save writes the snapshot to .evidence/last-upload.json.
func (s *Snapshot) Save(workspaceDir string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := filepath.Join(EvidenceDir(workspaceDir), snapshotFile)
	return os.WriteFile(path, data, 0o644)
}

// ChangedFiles returns the list of test.xml paths that are new or have a
// newer mtime than what was recorded in the snapshot.
func (s *Snapshot) ChangedFiles(xmlPaths []string) ([]string, error) {
	var changed []string
	for _, p := range xmlPaths {
		info, err := os.Stat(p)
		if err != nil {
			continue // file disappeared — skip
		}
		mtime := info.ModTime()
		if prev, ok := s.Files[p]; !ok || mtime.After(prev) {
			changed = append(changed, p)
		}
	}
	return changed, nil
}

// Record updates the snapshot with the current mtimes for the given paths.
func (s *Snapshot) Record(xmlPaths []string) {
	s.UploadedAt = time.Now().UTC()
	for _, p := range xmlPaths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		s.Files[p] = info.ModTime()
	}
}
