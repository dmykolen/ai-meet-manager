package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Lock keeps a second daemon from starting.
//
// This is not hygiene, it is a real failure that was observed: with two
// instances running, one meeting was recorded twice, and the second tap came up
// silent so its copy was filed as a note. Anybody who double-clicks the app
// while launchd already has it running would get the same. One machine, one
// listener.
type Lock struct{ path string }

// Take claims the lock, or reports who holds it.
func Take(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "mtd.lock")

	if held, by := heldBy(path); held {
		return nil, fmt.Errorf("another listener is already running (pid %d); "+
			"stop it first, or use the menu bar item it already put there", by)
	}
	// A stale lock is simply overwritten: the process it names is gone, and
	// refusing to start because of a file left by a crash would be worse than
	// the duplicate it is meant to prevent.
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return nil, err
	}
	return &Lock{path: path}, nil
}

func (l *Lock) Release() { _ = os.Remove(l.path) }

// heldBy reports whether the pid in the lock file is a process that still
// exists. Signal 0 asks the kernel that question without disturbing it.
func heldBy(path string) (bool, int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return false, 0
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}
	return proc.Signal(syscall.Signal(0)) == nil, pid
}
