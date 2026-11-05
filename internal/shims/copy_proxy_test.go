package shims

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useCopyProxy runs a test with the Windows proxy model on any platform.
func useCopyProxy(t *testing.T) (home, ext string) {
	t.Helper()
	old := copyProxy
	copyProxy = true
	t.Cleanup(func() { copyProxy = old })
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	ext = filepath.Join(home, "extension")
	if err := os.WriteFile(ext, []byte("extension v1"), 0755); err != nil {
		t.Fatal(err)
	}
	return home, ext
}

func TestWindowsCopyProxyLifecycle(t *testing.T) {
	_, ext := useCopyProxy(t)
	path, err := InstallNativeGH(ext)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("proxy is not a regular file copy: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "extension v1" {
		t.Fatalf("proxy content %q", got)
	}
	if !IsNativeGHInstalled() || NativeGHStale(ext) {
		t.Fatal("fresh copy not reported as installed and current")
	}

	// gh extension upgrade replaces the extension; the copy keeps the old bytes.
	if err := os.WriteFile(ext, []byte("extension v2"), 0755); err != nil {
		t.Fatal(err)
	}
	if !IsNativeGHInstalled() || !NativeGHStale(ext) {
		t.Fatal("stale copy must stay owned but be reported stale")
	}
	if _, err := InstallNativeGH(ext); err != nil {
		t.Fatalf("refresh over an owned copy: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "extension v2" || NativeGHStale(ext) {
		t.Fatalf("refresh did not replace the copy: %q", got)
	}
	if _, err := os.Lstat(path + ".old"); !os.IsNotExist(err) {
		t.Fatalf("set-aside copy left behind: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("shim directory holds more than the proxy: %v %v", entries, err)
	}
}

func TestWindowsCopyProxyRefusesForeignFile(t *testing.T) {
	_, ext := useCopyProxy(t)
	path, err := GHPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	// A user's own gh at the proxy path is not ours, whatever it contains.
	if err := os.WriteFile(path, []byte("someone's gh"), 0755); err != nil {
		t.Fatal(err)
	}
	if IsNativeGHInstalled() {
		t.Fatal("foreign file reported as the managed proxy")
	}
	if _, err := InstallNativeGH(ext); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("foreign file overwritten: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "someone's gh" {
		t.Fatalf("foreign file changed: %q", got)
	}
}

func TestWindowsCopyProxyKeepsUserFilesAndClearsSetAside(t *testing.T) {
	_, ext := useCopyProxy(t)
	path, err := InstallNativeGH(ext)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	// A user's own backup next to the proxy is not ours to delete.
	userBackup := path + ".old"
	if err := os.WriteFile(userBackup, []byte("mine"), 0644); err != nil {
		t.Fatal(err)
	}
	// A set-aside copy from an earlier refresh, no longer running.
	leftover := filepath.Join(dir, setAsidePrefix+"123")
	if err := os.WriteFile(leftover, []byte("extension v0"), 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(ext, []byte(fmt.Sprintf("extension v%d", i+2)), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := InstallNativeGH(ext); err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
	}
	if got, err := os.ReadFile(userBackup); err != nil || string(got) != "mine" {
		t.Fatalf("user backup changed: %q %v", got, err)
	}
	if _, err := os.Lstat(leftover); !os.IsNotExist(err) {
		t.Fatalf("earlier set-aside copy kept: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if name := entry.Name(); name != filepath.Base(path) && name != filepath.Base(userBackup) {
			t.Fatalf("unexpected file left in the shim directory: %s", name)
		}
	}
}
