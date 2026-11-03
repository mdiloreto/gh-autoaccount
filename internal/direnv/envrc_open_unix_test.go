//go:build linux || darwin

package direnv

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestManualEnvrcInspectionRejectsSwappedSymlinkAndFIFO(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".envrc")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unmanaged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unmanaged\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireManualEnvrcCleanup(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnvrcNoFollow(path); err == nil {
		t.Fatal("followed substituted symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := RequireManualEnvrcCleanup(dir); err == nil {
		t.Fatal("accepted FIFO")
	}
}
