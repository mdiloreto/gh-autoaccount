package shims

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeShimFile seeds the gh path with raw bytes so tests do not depend on a
// generator for the retired shell shim.
func writeShimFile(t *testing.T, content string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path, err := GHPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckGHReplaceableRefusesUnmanagedExecutable(t *testing.T) {
	original := "#!/bin/sh\n# gh-autoprofile-gh-shim\necho real-gh\n"
	path := writeShimFile(t, original)
	if err := CheckGHReplaceable(); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("CheckGHReplaceable error = %v, want unmanaged executable refusal", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatalf("unmanaged executable changed: %q, %v", data, err)
	}
	if IsGHInstalled() {
		t.Fatal("unmanaged executable reported as installed shim")
	}
}

func TestOldManagedShimFilesStayReplaceable(t *testing.T) {
	cases := map[string]string{
		"signed": "#!/usr/bin/env bash\n" + shimSignature + "\nset -euo pipefail\nif [[ -n \"${GH_AUTOPROFILE_DISABLE_SHIM:-}\" ]]; then exec '/usr/bin/gh' \"$@\"; fi\nGH_AUTOPROFILE_REAL_GH='/usr/bin/gh' exec '/tmp/old' __shim-gh \"$@\"\n",
		"unsigned": `#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${GH_AUTOPROFILE_DISABLE_SHIM:-}" ]]; then exec "/usr/bin/gh" "$@"; fi
GH_AUTOPROFILE_REAL_GH="/usr/bin/gh" exec "/tmp/old-autoprofile" __shim-gh "$@"
`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			writeShimFile(t, content)
			if err := CheckGHReplaceable(); err != nil {
				t.Fatalf("CheckGHReplaceable: %v", err)
			}
			if !IsGHInstalled() {
				t.Fatal("old managed shim not recognized")
			}
		})
	}
}

func TestInstallNativeGHReplacesOldManagedShim(t *testing.T) {
	if !NativeGHSupported() {
		t.Skip("native unsupported")
	}
	path := writeShimFile(t, "#!/usr/bin/env bash\n"+shimSignature+"\nset -euo pipefail\nexec x __shim-gh \"$@\"\n")
	ext := filepath.Join(filepath.Dir(path), "extension")
	if err := os.WriteFile(ext, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallNativeGH(ext); err != nil {
		t.Fatal(err)
	}
	if !IsNativeGHInstalled() {
		t.Fatal("old shim file was not replaced by the native proxy")
	}
}

func TestCheckGHReplaceableRefusesSymlink(t *testing.T) {
	path := writeShimFile(t, "unused")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(path), "real-gh")
	if err := os.WriteFile(target, []byte("unchanged"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := CheckGHReplaceable(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("CheckGHReplaceable error = %v, want symlink refusal", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "unchanged" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
}

func TestInjectPath(t *testing.T) {
	rcPath := filepath.Join(t.TempDir(), ".zshrc")
	shimDir := "/tmp/gh-autoprofile-shims"

	if err := InjectPath(rcPath, shimDir); err != nil {
		t.Fatalf("InjectPath failed: %v", err)
	}

	data, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("cannot read rc file: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, shimMarkerStart) {
		t.Fatal("shim marker start not found")
	}
	if !strings.Contains(s, `export PATH='/tmp/gh-autoprofile-shims':"$PATH"`) {
		t.Fatalf("shim PATH export not found: %s", s)
	}
}

func TestShimsRejectRelativeHome(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".bashrc"), []byte(shimMarkerStart), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", ".")
	t.Setenv("USERPROFILE", ".")
	if got, err := os.UserHomeDir(); err != nil || got != "." {
		t.Skipf("os.UserHomeDir does not use HOME or USERPROFILE: %q, %v", got, err)
	}
	t.Chdir(cwd)
	if runtime.GOOS != "windows" {
		if dir, err := Dir(); err == nil || dir != "" {
			t.Fatalf("Dir() = %q, %v; want a home-directory error", dir, err)
		}
	}
	if CheckPathBlockInstalled() {
		t.Fatal("read a shim PATH block from the relative home")
	}
}
