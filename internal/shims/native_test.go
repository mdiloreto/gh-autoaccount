package shims

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNativeLifecycle(t *testing.T) {
	if !NativeGHSupported() {
		t.Skip("native unsupported")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	first := filepath.Join(home, "extension 'one")
	second := filepath.Join(home, "extension two")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := InstallGH(first, "/usr/bin/gh"); err != nil {
		t.Fatal(err)
	}
	path, err := InstallNativeGH(first)
	if err != nil {
		t.Fatal(err)
	}
	if !IsNativeGHInstalled() || !IsGHInstalled() {
		t.Fatal("native shim not detected")
	}
	if ok, err := TargetsGH("/different/gh"); err != nil || !ok {
		t.Fatalf("runtime target: %v %v", ok, err)
	}
	if _, err := InstallNativeGH(first); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if IsNativeGHInstalled() || IsGHInstalled() {
		t.Fatal("broken shim reported healthy")
	}
	if _, err := InstallNativeGH(second); err != nil {
		t.Fatalf("relocation: %v", err)
	}
	if target, err := os.Readlink(path); err != nil || target != second {
		t.Fatalf("target %q: %v", target, err)
	}
	if _, err := InstallGH(second, "/usr/bin/gh"); err != nil {
		t.Fatal(err)
	}
	if IsNativeGHInstalled() || !IsGHInstalled() {
		t.Fatal("legacy transition failed")
	}
}

func TestNativeRefusesUnknownLinksAndBrokenRecord(t *testing.T) {
	if !NativeGHSupported() {
		t.Skip("native unsupported")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	ext := filepath.Join(home, "extension")
	if err := os.WriteFile(ext, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	path, _ := GHPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallNativeGH(ext); err == nil {
		t.Fatal("replaced unknown dangling link")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallNativeGH(ext); err != nil {
		t.Fatal(err)
	}
	record, _ := nativeRecordPath()
	if err := os.WriteFile(record, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if IsNativeGHInstalled() {
		t.Fatal("corrupt record considered healthy")
	}
	if _, err := InstallNativeGH(ext); err == nil {
		t.Fatal("replaced link with corrupt record")
	}
}

func TestNativeRefusesUnmanagedFileAndDirectory(t *testing.T) {
	if !NativeGHSupported() {
		t.Skip("native unsupported")
	}
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			ext := filepath.Join(home, "extension")
			if err := os.WriteFile(ext, []byte("#!/bin/sh\n"), 0755); err != nil {
				t.Fatal(err)
			}
			path, _ := GHPath()
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				if err := os.WriteFile(path, []byte("#!/bin/sh\necho untouched\n"), 0755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(path, 0755); err != nil {
				t.Fatal(err)
			}
			if _, err := InstallNativeGH(ext); err == nil {
				t.Fatal("replaced unmanaged path")
			}
			if kind == "file" {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != "#!/bin/sh\necho untouched\n" {
					t.Fatalf("unmanaged file changed: %q, %v", data, err)
				}
			}
		})
	}
}
