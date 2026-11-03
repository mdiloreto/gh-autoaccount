package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLockMutationsRecordsOwner(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	unlock, err := LockMutations()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	dir, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "mutation.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock owner = %q, want PID %d", data, os.Getpid())
	}
}

func TestLockMutationsReportsOwnerAndAge(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mutation.lock")
	if err := os.WriteFile(path, []byte("4242\n"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	_, err = LockMutations()
	if err == nil || !strings.Contains(err.Error(), "held by PID 4242") || !strings.Contains(err.Error(), "age 2m") {
		t.Fatalf("lock diagnostic = %v", err)
	}
}

func TestWriteAtomicModeSetsRequestedPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script")
	if err := WriteAtomicMode(path, []byte("content"), 0755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Fatalf("mode = %o, want 755", got)
	}
}

func TestWriteAtomicPreserveModeFollowsExistingSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed-rc")
	link := filepath.Join(dir, ".bashrc")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomicPreserveMode(link, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("RC symlink was replaced")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "new" {
		t.Fatalf("target = %q, %v", data, err)
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := targetInfo.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}
