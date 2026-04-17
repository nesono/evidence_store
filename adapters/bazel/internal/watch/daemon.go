package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const pidFile = "watch.pid"

// WritePID writes the current process PID to .evidence/watch.pid.
func WritePID(workspaceDir string) error {
	path := filepath.Join(EvidenceDir(workspaceDir), pidFile)
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// RemovePID removes the PID file.
func RemovePID(workspaceDir string) {
	os.Remove(filepath.Join(EvidenceDir(workspaceDir), pidFile))
}

// ReadPID reads the PID from .evidence/watch.pid.
// Returns 0 if the file doesn't exist or is invalid.
func ReadPID(workspaceDir string) int {
	data, err := os.ReadFile(filepath.Join(EvidenceDir(workspaceDir), pidFile))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// IsRunning checks if a watcher process is alive for the given workspace.
func IsRunning(workspaceDir string) (int, bool) {
	pid := ReadPID(workspaceDir)
	if pid == 0 {
		return 0, false
	}
	// Check if the process is alive by sending signal 0.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		// Process doesn't exist — stale PID file.
		RemovePID(workspaceDir)
		return pid, false
	}
	return pid, true
}

// StopWatcher sends SIGTERM to the running watcher process.
func StopWatcher(workspaceDir string) error {
	pid, alive := IsRunning(workspaceDir)
	if !alive {
		return fmt.Errorf("no watcher running (stale pid: %d)", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to %d: %w", pid, err)
	}
	return nil
}
