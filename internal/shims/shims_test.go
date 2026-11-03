package shims

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallGH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	shimPath, err := InstallGH("/tmp/gh-autoprofile", "/usr/bin/gh")
	if err != nil {
		t.Fatalf("InstallGH failed: %v", err)
	}
	expectedPath := filepath.Join(home, ".local", "bin", "gh")
	if shimPath != expectedPath {
		t.Fatalf("InstallGH path = %q, want %q", shimPath, expectedPath)
	}

	data, err := os.ReadFile(shimPath)
	if err != nil {
		t.Fatalf("cannot read shim: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `GH_AUTOPROFILE_REAL_GH='/usr/bin/gh'`) {
		t.Fatalf("shim missing real gh path: %s", s)
	}
	if !strings.Contains(s, `GH_PATH='/usr/bin/gh' GH_AUTOPROFILE_REAL_GH='/usr/bin/gh'`) {
		t.Fatalf("shim missing fixed GH_PATH: %s", s)
	}
	if !strings.Contains(s, `exec '/tmp/gh-autoprofile' __shim-gh "$@"`) {
		t.Fatalf("shim missing extension dispatch: %s", s)
	}
}

func TestInstallGHRefusesUnmanagedExecutable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".local", "bin", "gh")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	original := []byte("#!/bin/sh\n# gh-autoprofile-gh-shim\necho real-gh\n")
	if err := os.WriteFile(path, original, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGH("/tmp/gh-autoprofile", "/usr/bin/gh"); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("InstallGH error = %v, want unmanaged executable refusal", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(original) {
		t.Fatalf("unmanaged executable changed: %q, %v", data, err)
	}
	if IsGHInstalled() {
		t.Fatal("unmanaged executable reported as installed shim")
	}
}

func TestInstallGHRefreshesManagedShim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := InstallGH("/tmp/old-autoprofile", "/usr/bin/gh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGH("/tmp/new-autoprofile", "/usr/bin/gh"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "/tmp/new-autoprofile") || strings.Contains(string(data), "/tmp/old-autoprofile") {
		t.Fatalf("managed shim was not refreshed: %s, %v", data, err)
	}
	if !IsGHInstalled() {
		t.Fatal("managed shim was not detected")
	}
	current, err := TargetsGH("/usr/bin/gh")
	if err != nil || !current {
		t.Fatalf("installed shim target = %v, %v", current, err)
	}
	current, err = TargetsGH("/opt/gh/bin/gh")
	if err != nil || current {
		t.Fatalf("stale shim target = %v, %v", current, err)
	}
}

func TestInstallGHRefreshesLegacyManagedShim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".local", "bin", "gh")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${GH_AUTOPROFILE_DISABLE_SHIM:-}" ]]; then exec "/usr/bin/gh" "$@"; fi
GH_AUTOPROFILE_REAL_GH="/usr/bin/gh" exec "/tmp/old-autoprofile" __shim-gh "$@"
`
	if err := os.WriteFile(path, []byte(legacy), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGH("/tmp/new-autoprofile", "/usr/bin/gh"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), shimSignature) || !strings.Contains(string(data), "/tmp/new-autoprofile") {
		t.Fatalf("legacy shim was not refreshed: %s, %v", data, err)
	}
}

func TestTargetsGHRejectsLegacyShim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".local", "bin", "gh")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `#!/usr/bin/env bash
set -euo pipefail
if [[ -n "${GH_AUTOPROFILE_DISABLE_SHIM:-}" ]]; then exec "/usr/bin/gh" "$@"; fi
GH_AUTOPROFILE_REAL_GH="/usr/bin/gh" exec "/tmp/old-autoprofile" __shim-gh "$@"
`
	if err := os.WriteFile(path, []byte(legacy), 0755); err != nil {
		t.Fatal(err)
	}
	current, err := TargetsGH("/usr/bin/gh")
	if err != nil || current {
		t.Fatalf("legacy shim target = %v, %v", current, err)
	}
}

func TestRefreshPriorSignedShim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := GHPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	prior := "#!/usr/bin/env bash\n" + shimSignature + "\nset -euo pipefail\nif [[ -n \"${GH_AUTOPROFILE_DISABLE_SHIM:-}\" ]]; then exec '/usr/bin/gh' \"$@\"; fi\nGH_AUTOPROFILE_REAL_GH='/usr/bin/gh' exec '/tmp/old' __shim-gh \"$@\"\n"
	if err := os.WriteFile(path, []byte(prior), 0755); err != nil {
		t.Fatal(err)
	}
	if !IsGHInstalled() {
		t.Fatal("prior generated shim not recognized")
	}
	if current, err := TargetsGH("/usr/bin/gh"); err != nil || current {
		t.Fatalf("prior shim wrongly considered current: %v %v", current, err)
	}
	if _, err := InstallGH("/tmp/new", "/usr/bin/gh"); err != nil {
		t.Fatal(err)
	}
	if current, err := TargetsGH("/usr/bin/gh"); err != nil || !current {
		t.Fatalf("updated shim target: %v %v", current, err)
	}
}

func TestShimPinsGHAgainstAttackerPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	trusted := filepath.Join(home, "trusted path's")
	attackerDir := filepath.Join(home, "attacker")
	for _, dir := range []string{trusted, attackerDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	realGH := filepath.Join(trusted, "real gh")
	extension := filepath.Join(trusted, "extension gh")
	attacker := filepath.Join(attackerDir, "gh")
	log := filepath.Join(home, "attacker-log")
	output := filepath.Join(home, "output")
	for path, script := range map[string]string{
		realGH:    "#!/bin/sh\nprintf 'real:%s:%s\\n' \"$*\" \"${GH_TOKEN:-none}\" > \"$TEST_OUTPUT\"\n",
		attacker:  "#!/bin/sh\nprintf '%s\\n' \"$*\" \"${GH_TOKEN:-none}\" > \"$ATTACKER_LOG\"\nexit 1\n",
		extension: "#!/bin/sh\ntest \"$GH_PATH\" = \"$EXPECTED_GH\" || exit 2\ntest \"$GH_AUTOPROFILE_REAL_GH\" = \"$EXPECTED_GH\" || exit 3\nshift\nGH_TOKEN=selected-token exec \"$GH_PATH\" \"$@\"\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	shim, err := InstallGH(extension, realGH)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", shim).CombinedOutput(); err != nil {
		t.Fatalf("invalid generated shell: %s %v", out, err)
	}
	t.Setenv("PATH", attackerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_PATH", attacker)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", attacker)
	t.Setenv("ATTACKER_LOG", log)
	t.Setenv("EXPECTED_GH", realGH)
	t.Setenv("TEST_OUTPUT", output)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_AUTOPROFILE_DISABLE_SHIM", "")
	if out, err := exec.Command(shim, "api", "user").CombinedOutput(); err != nil {
		t.Fatalf("shim: %s %v", out, err)
	}
	if out, err := os.ReadFile(output); err != nil || string(out) != "real:api user:selected-token\n" {
		t.Fatalf("dispatch: %q %v", out, err)
	}
	t.Setenv("GH_AUTOPROFILE_DISABLE_SHIM", "1")
	if out, err := exec.Command(shim, "auth", "status").CombinedOutput(); err != nil {
		t.Fatalf("bypass: %s %v", out, err)
	}
	if out, err := os.ReadFile(output); err != nil || string(out) != "real:auth status:none\n" {
		t.Fatalf("bypass dispatch: %q %v", out, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("attacker ran or captured token: %v", err)
	}
}

func TestInstallGHQuotesLiteralPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	literalDir := filepath.Join(home, "space $(touch PWNED) quote's")
	if err := os.MkdirAll(literalDir, 0755); err != nil {
		t.Fatal(err)
	}
	realGH := filepath.Join(literalDir, "real gh")
	extension := filepath.Join(literalDir, "gh autoaccount")
	output := filepath.Join(home, "output")
	realScript := "#!/bin/sh\nprintf 'REAL:%s\\n' \"$*\" > \"$TEST_OUTPUT\"\n"
	extensionScript := "#!/bin/sh\nprintf 'EXT:%s:%s\\n' \"$GH_AUTOPROFILE_REAL_GH\" \"$*\" > \"$TEST_OUTPUT\"\n"
	for path, content := range map[string]string{realGH: realScript, extension: extensionScript} {
		if err := os.WriteFile(path, []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	shim, err := InstallGH(extension, realGH)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(shim, "api", "user")
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "TEST_OUTPUT="+output)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shim failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "EXT:"+realGH+":__shim-gh api user\n" {
		t.Fatalf("extension invocation = %q, %v", data, err)
	}

	cmd = exec.Command(shim, "auth", "status")
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "TEST_OUTPUT="+output, "GH_AUTOPROFILE_DISABLE_SHIM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("disabled shim failed: %v\n%s", err, out)
	}
	data, err = os.ReadFile(output)
	if err != nil || string(data) != "REAL:auth status\n" {
		t.Fatalf("real gh invocation = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, "PWNED")); !os.IsNotExist(err) {
		t.Fatalf("literal path executed command substitution: %v", err)
	}
}

func TestInstallGHRefusesSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "real-gh")
	if err := os.WriteFile(target, []byte("unchanged"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "gh")); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallGH("/tmp/gh-autoprofile", "/usr/bin/gh"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("InstallGH error = %v, want symlink refusal", err)
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
