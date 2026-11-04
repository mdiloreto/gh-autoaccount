//go:build linux || darwin

package skills

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestInstallRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	managed := filepath.Join(dir, "gh-autoaccount")
	if err := os.Mkdir(managed, 0755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(managed, "SKILL.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := install(dir, "content"); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestInstallPermissionFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, _, err := install(dir, "content"); err == nil {
		t.Fatal("expected permission failure")
	}
}

func TestInstallExistingUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	dir := t.TempDir()
	path, _, err := install(dir, "content")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if _, _, err := install(dir, "content"); err == nil {
		t.Fatal("expected read permission failure")
	}
}
