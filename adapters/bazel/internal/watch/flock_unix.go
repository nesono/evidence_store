//go:build !windows

package watch

import (
	"os"
	"syscall"
)

func syscallFlock(f *os.File, how int) error {
	return syscall.Flock(int(f.Fd()), how)
}

// isFileLocked tries to acquire an exclusive lock on the file.
// Returns true if the file is locked (Bazel is running).
func isFileLocked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false // file doesn't exist = not locked
	}
	defer f.Close()

	// Try a non-blocking exclusive lock.
	err = syscallFlock(f, syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		return true // lock held by another process (Bazel)
	}
	// We got the lock — Bazel is not running. Release it.
	syscallFlock(f, syscall.LOCK_UN)
	return false
}
