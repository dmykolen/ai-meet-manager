package session

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestASecondListenerIsRefused(t *testing.T) {
	// Observed, not hypothetical: with two instances running, one meeting was
	// recorded twice and the second tap came up silent, so its copy was filed
	// as a note.
	dir := t.TempDir()
	first, err := Take(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	// A different pid in the file is what a second process would see.
	os.WriteFile(filepath.Join(dir, "mtd.lock"), []byte(strconv.Itoa(os.Getppid())), 0o644)

	_, err = Take(dir)
	if err == nil {
		t.Fatal("a second listener was allowed to start")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("the error does not say what is wrong: %v", err)
	}
}

func TestALockLeftByACrashIsTakenOver(t *testing.T) {
	// Refusing to start because of a file left by a crash would be worse than
	// the duplicate the lock exists to prevent.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mtd.lock"), []byte("999999"), 0o644)

	lock, err := Take(dir)
	if err != nil {
		t.Fatalf("a stale lock blocked startup: %v", err)
	}
	lock.Release()
}

func TestReleasingLetsTheNextOneStart(t *testing.T) {
	dir := t.TempDir()
	first, err := Take(dir)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()

	second, err := Take(dir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	second.Release()
}

func TestAGarbledLockDoesNotStopTheDaemon(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "mtd.lock"), []byte("not a pid"), 0o644)

	lock, err := Take(dir)
	if err != nil {
		t.Fatalf("a corrupt lock file blocked startup: %v", err)
	}
	lock.Release()
}
