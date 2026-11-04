package shims

import (
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
