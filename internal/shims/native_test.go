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

func miseShimHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	return home
}

func writeExtension(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestMiseGHDirFollowsXDGDataHome(t *testing.T) {
	home := miseShimHome(t)
	if dir, err := MiseGHDir(); err != nil || dir != filepath.Join(home, "data", "gh-autoaccount", "bin") {
		t.Fatalf("XDG data dir: %q, %v", dir, err)
	}
	t.Setenv("XDG_DATA_HOME", "")
	if dir, err := MiseGHDir(); err != nil || dir != filepath.Join(home, ".local", "share", "gh-autoaccount", "bin") {
		t.Fatalf("default data dir: %q, %v", dir, err)
	}
}

func TestMiseGHDirHoldsOnlyTheShimAndSurvivesRelocation(t *testing.T) {
	if !NativeGHSupported() {
		t.Skip("native unsupported")
	}
	home := miseShimHome(t)
	first, second := filepath.Join(home, "extension-one"), filepath.Join(home, "extension-two")
	writeExtension(t, first)
	writeExtension(t, second)
	native, err := InstallNativeGH(first)
	if err != nil {
		t.Fatal(err)
	}
	mise, err := InstallMiseGH(first)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := MiseGHDir()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "gh" {
		t.Fatalf("gh-only dir must hold only gh: %v, %v", entries, err)
	}
	if !IsNativeGHInstalled() || !IsMiseGHInstalled() {
		t.Fatal("shims not detected")
	}
	for _, path := range []string{native, mise} {
		if !IsManagedGHShim(path) {
			t.Fatalf("%s not recognized as a managed shim", path)
		}
	}
	if IsManagedGHShim(first) || IsManagedGHShim(filepath.Join(home, "missing", "gh")) {
		t.Fatal("unrelated paths recognized as managed shims")
	}

	// Relocating the extension through one shim must not orphan the other.
	if _, err := InstallNativeGH(second); err != nil {
		t.Fatal(err)
	}
	if !IsMiseGHInstalled() {
		t.Fatal("native relocation orphaned the gh-only shim")
	}
	if _, err := InstallMiseGH(second); err != nil {
		t.Fatalf("gh-only shim relocation: %v", err)
	}
	if got := nativeTargets(); len(got) != 1 || got[0] != second {
		t.Fatalf("stale ownership targets retained: %v", got)
	}
}

func TestMiseGHRefusesUnmanagedPathsAndRemoveLeavesThemAlone(t *testing.T) {
	if !NativeGHSupported() {
		t.Skip("native unsupported")
	}
	for _, kind := range []string{"file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			home := miseShimHome(t)
			ext := filepath.Join(home, "extension")
			writeExtension(t, ext)
			path, _ := MiseGHPath()
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "file":
				if err := os.WriteFile(path, []byte("#!/bin/sh\necho untouched\n"), 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(ext, path); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.Mkdir(path, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := CheckMiseGHReplaceable(); err == nil {
				t.Fatal("unmanaged path considered replaceable")
			}
			if _, err := InstallMiseGH(ext); err == nil {
				t.Fatal("replaced unmanaged path")
			}
			if removed, err := RemoveMiseGH(); err != nil || removed {
				t.Fatalf("removed unmanaged path: %v, %v", removed, err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("unmanaged path disappeared: %v", err)
			}
		})
	}
}

func TestRemoveMiseGHRemovesManagedShimAndEmptyDirs(t *testing.T) {
	if !NativeGHSupported() {
		t.Skip("native unsupported")
	}
	home := miseShimHome(t)
	ext := filepath.Join(home, "extension")
	writeExtension(t, ext)
	if _, err := InstallMiseGH(ext); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveMiseGH(); err != nil || !removed {
		t.Fatalf("remove: %v, %v", removed, err)
	}
	dir, _ := MiseGHDir()
	if _, err := os.Stat(filepath.Dir(dir)); !os.IsNotExist(err) {
		t.Fatalf("empty directories retained: %v", err)
	}
	if removed, err := RemoveMiseGH(); err != nil || removed {
		t.Fatalf("second remove: %v, %v", removed, err)
	}
}
