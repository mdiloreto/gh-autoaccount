package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

// fakeMise logs its argv and edits a plain-text stand-in for mise's global
// config, one registered directory per line.
type fakeMise struct {
	path, log, toml, fail, failAfter string
}

const fakeMiseScript = `#!/bin/sh
printf '%s\n' "$*" >> "$MISE_FAKE_LOG"
if [ -e "$MISE_FAKE_FAIL" ]; then echo "fake mise failure" >&2; exit 1; fi
case "$1 $2 $3 $4" in
"config get -g env._.path")
  if [ -n "$MISE_FAKE_GET_RAW" ]; then printf '%s\n' "$MISE_FAKE_GET_RAW"; exit 0; fi
  if [ -s "$MISE_FAKE_TOML" ]; then
    printf '['; sed 's/.*/"&"/' "$MISE_FAKE_TOML" | paste -sd, | tr -d '\n'; printf ']\n'
    exit 0
  fi
  echo "mise ERROR Key not found: env._.path" >&2; exit 1 ;;
esac
case "$1 $2 $3 $4 $5" in
"config set -g --append env._.path")
  grep -qxF -- "$6" "$MISE_FAKE_TOML" 2>/dev/null || printf '%s\n' "$6" >> "$MISE_FAKE_TOML" ;;
"config set -g --remove env._.path")
  if [ -f "$MISE_FAKE_TOML" ]; then
    grep -vxF -- "$6" "$MISE_FAKE_TOML" > "$MISE_FAKE_TOML.new"
    mv "$MISE_FAKE_TOML.new" "$MISE_FAKE_TOML"
  fi ;;
"exec -- sh -c command -v gh")
  [ "$MISE_EXEC_AUTO_INSTALL" = 0 ] || { echo "mise exec could auto-install tools" >&2; exit 3; }
  printf '%s\n' "$MISE_FAKE_EXEC_GH" ;;
*) echo "unexpected mise invocation: $*" >&2; exit 2 ;;
esac
# A mise that wrote its config and then failed.
if [ -e "$MISE_FAKE_FAIL_AFTER" ]; then echo "fake mise failure after write" >&2; exit 1; fi
`

func installFakeMise(t *testing.T, home string) *fakeMise {
	t.Helper()
	fm := &fakeMise{
		path:      filepath.Join(home, "fake-mise", "mise"),
		log:       filepath.Join(home, "fake-mise", "argv.log"),
		toml:      filepath.Join(home, "fake-mise", "config.toml"),
		fail:      filepath.Join(home, "fake-mise", "fail"),
		failAfter: filepath.Join(home, "fake-mise", "fail-after-write"),
	}
	if err := os.MkdirAll(filepath.Dir(fm.path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fm.path, []byte(fakeMiseScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISE_FAKE_LOG", fm.log)
	t.Setenv("MISE_FAKE_TOML", fm.toml)
	t.Setenv("MISE_FAKE_FAIL", fm.fail)
	t.Setenv("MISE_FAKE_FAIL_AFTER", fm.failAfter)
	t.Setenv("MISE_FAKE_EXEC_GH", "")
	t.Setenv("MISE_FAKE_GET_RAW", "")
	miseExecutable = func() (string, error) { return fm.path, nil }
	return fm
}

// touch makes one of the fake's failure switches active.
func touch(t *testing.T, flag string) {
	t.Helper()
	if err := os.WriteFile(flag, nil, 0600); err != nil {
		t.Fatal(err)
	}
}

// calls returns the logged invocations other than read-only config lookups.
func (fm *fakeMise) calls(t *testing.T) []string {
	t.Helper()
	var calls []string
	for _, line := range fileLines(t, fm.log) {
		if !strings.HasPrefix(line, "config get ") {
			calls = append(calls, line)
		}
	}
	return calls
}

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func runSetupArgs(t *testing.T, args ...string) {
	t.Helper()
	command := NewSetupCmd()
	command.SetArgs(args)
	captureOnboarding(t, command.Execute)
}

func runSetupArgsErr(args ...string) error {
	command := NewSetupCmd()
	command.SetArgs(args)
	return command.Execute()
}

func runDoctorFix(t *testing.T) {
	t.Helper()
	doctor := NewDoctorCmd()
	doctor.SetArgs([]string{"--fix"})
	captureOnboarding(t, doctor.Execute)
}

func loadSettings(t *testing.T) config.Settings {
	t.Helper()
	settings, err := config.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func miseDir(t *testing.T) string {
	t.Helper()
	dir, err := shims.MiseGHDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func requireNative(t *testing.T) {
	t.Helper()
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
}

func TestSetupMiseLifecycle(t *testing.T) {
	requireNative(t)
	home := onboardingHome(t)
	fm := installFakeMise(t, home)
	dir := miseDir(t)
	register := "config set -g --append env._.path " + dir
	unregister := "config set -g --remove env._.path " + dir

	runSetupArgs(t, "--native", "--no-shell-rc", "--mise")
	if settings := loadSettings(t); !settings.Mise || !settings.Native {
		t.Fatalf("mise not persisted: %+v", settings)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "gh" || !shims.IsMiseGHInstalled() || !shims.IsNativeGHInstalled() {
		t.Fatalf("shims not installed (%v, %v)", entries, err)
	}
	if got := fm.calls(t); !reflect.DeepEqual(got, []string{register}) {
		t.Fatalf("enable argv: %q", got)
	}
	if got := fileLines(t, fm.toml); !reflect.DeepEqual(got, []string{dir}) {
		t.Fatalf("mise config: %q", got)
	}

	// Migration and doctor --fix re-ensure, and the registration stays single.
	runSetupArgs(t, "--migrate")
	runDoctorFix(t)
	if got := fm.calls(t); !reflect.DeepEqual(got, []string{register, register, register}) {
		t.Fatalf("re-ensure argv: %q", got)
	}
	if got := fileLines(t, fm.toml); !reflect.DeepEqual(got, []string{dir}) {
		t.Fatalf("registration duplicated: %q", got)
	}
	if !loadSettings(t).Mise || !shims.IsMiseGHInstalled() {
		t.Fatal("migration dropped mise integration")
	}

	runSetupArgs(t, "--mise=false")
	if got := fm.calls(t); len(got) != 4 || got[3] != unregister {
		t.Fatalf("disable argv: %q", got)
	}
	if got := fileLines(t, fm.toml); len(got) != 0 {
		t.Fatalf("registration not removed: %q", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) || shims.IsMiseGHInstalled() || loadSettings(t).Mise || !shims.IsNativeGHInstalled() {
		t.Fatalf("disable left state behind: %v", err)
	}

	// Without the persisted opt-in, migration must never enable it.
	runSetupArgs(t, "--migrate")
	runDoctorFix(t)
	if got := fm.calls(t); len(got) != 4 {
		t.Fatalf("mise invoked without opt-in: %q", got)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("gh-only shim recreated without opt-in: %v", err)
	}
}

func TestSetupMiseRollback(t *testing.T) {
	requireNative(t)
	register := func(dir string) string { return "config set -g --append env._.path " + dir }
	unregister := func(dir string) string { return "config set -g --remove env._.path " + dir }
	t.Run("config read fails", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		touch(t, fm.fail)
		if err := runSetupArgsErr("--native", "--no-shell-rc", "--mise"); err == nil || !strings.Contains(err.Error(), "fake mise failure") {
			t.Fatalf("expected the mise failure to surface: %v", err)
		}
		if _, err := os.Stat(miseDir(t)); !os.IsNotExist(err) || shims.IsNativeGHInstalled() || loadSettings(t).Mise || loadSettings(t).Native {
			t.Fatalf("failed setup left state behind: %v", err)
		}
		if got := fm.calls(t); len(got) != 0 {
			t.Fatalf("mise edited config after a failed read: %q", got)
		}
	})

	// The mise command wrote its config and then failed: the entry must go.
	t.Run("registration fails after writing", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		touch(t, fm.failAfter)
		if err := runSetupArgsErr("--native", "--no-shell-rc", "--mise"); err == nil || !strings.Contains(err.Error(), "failure after write") {
			t.Fatalf("expected the mise failure to surface: %v", err)
		}
		dir := miseDir(t)
		if _, err := os.Stat(dir); !os.IsNotExist(err) || shims.IsNativeGHInstalled() || loadSettings(t).Mise {
			t.Fatalf("failed setup left state behind: %v", err)
		}
		if got := fm.calls(t); !reflect.DeepEqual(got, []string{register(dir), unregister(dir)}) || len(fileLines(t, fm.toml)) != 0 {
			t.Fatalf("argv %q, config %q", got, fileLines(t, fm.toml))
		}
	})

	// Rollback restores what mise's config actually held, not what settings imply.
	t.Run("entry present before setup survives rollback", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		dir := miseDir(t)
		if err := os.MkdirAll(filepath.Dir(fm.toml), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fm.toml, []byte(dir+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		breakMigration(t)
		if err := runSetupArgsErr("--no-shell-rc", "--mise", "--git-identity"); err == nil {
			t.Fatal("expected migration failure")
		}
		if got := fm.calls(t); !reflect.DeepEqual(got, []string{register(dir)}) || !reflect.DeepEqual(fileLines(t, fm.toml), []string{dir}) {
			t.Fatalf("pre-existing entry lost: %q, %q", got, fileLines(t, fm.toml))
		}
	})

	t.Run("later step fails after registration", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		breakMigration(t)
		if err := runSetupArgsErr("--no-shell-rc", "--mise", "--git-identity"); err == nil {
			t.Fatal("expected migration failure")
		}
		dir := miseDir(t)
		if got := fm.calls(t); !reflect.DeepEqual(got, []string{register(dir), unregister(dir)}) {
			t.Fatalf("rollback argv: %q", got)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) || len(fileLines(t, fm.toml)) != 0 || loadSettings(t).Mise || shims.IsNativeGHInstalled() {
			t.Fatalf("rollback incomplete: %v", err)
		}
	})

	t.Run("later step fails while already enabled", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		runSetupArgs(t, "--native", "--no-shell-rc", "--mise")
		breakMigration(t)
		if err := runSetupArgsErr("--migrate", "--git-identity"); err == nil {
			t.Fatal("expected migration failure")
		}
		dir := miseDir(t)
		if got := fm.calls(t); !reflect.DeepEqual(got, []string{register(dir), register(dir)}) {
			t.Fatalf("a pre-existing registration must survive rollback: %q", got)
		}
		if got := fileLines(t, fm.toml); !reflect.DeepEqual(got, []string{dir}) || !loadSettings(t).Mise || !shims.IsMiseGHInstalled() {
			t.Fatalf("previous state not restored: %q", got)
		}
	})

	t.Run("disable fails after unregistering", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		runSetupArgs(t, "--native", "--no-shell-rc", "--mise")
		breakMigration(t)
		if err := runSetupArgsErr("--mise=false", "--git-identity"); err == nil {
			t.Fatal("expected migration failure")
		}
		dir := miseDir(t)
		if got := fm.calls(t); !reflect.DeepEqual(got, []string{register(dir), unregister(dir), register(dir)}) {
			t.Fatalf("disable rollback argv: %q", got)
		}
		if got := fileLines(t, fm.toml); !reflect.DeepEqual(got, []string{dir}) || !loadSettings(t).Mise || !shims.IsMiseGHInstalled() {
			t.Fatalf("disable not rolled back: %q", got)
		}
	})
}

func TestSetupMiseRequiresNativeAndMise(t *testing.T) {
	requireNative(t)
	t.Run("native=false with mise", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		err := runSetupArgsErr("--native=false", "--mise")
		if err == nil || !strings.Contains(err.Error(), legacyRemoved) {
			t.Fatalf("expected the retired-mode error: %v", err)
		}
		if len(fm.calls(t)) != 0 || loadSettings(t).Mise {
			t.Fatal("mise touched by a refused setup")
		}
	})
	t.Run("native=false while enabled", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		runSetupArgs(t, "--native", "--no-shell-rc", "--mise")
		before := fm.calls(t)
		err := runSetupArgsErr("--native=false")
		if err == nil || !strings.Contains(err.Error(), legacyRemoved) {
			t.Fatalf("expected the retired-mode error: %v", err)
		}
		if settings := loadSettings(t); !settings.Native || !settings.Mise || !shims.IsMiseGHInstalled() || !reflect.DeepEqual(before, fm.calls(t)) {
			t.Fatalf("refused transition changed state: %+v", settings)
		}
	})
	t.Run("without mise executable", func(t *testing.T) {
		onboardingHome(t)
		err := runSetupArgsErr("--native", "--no-shell-rc", "--mise")
		if err == nil || !strings.Contains(err.Error(), "mise executable") {
			t.Fatalf("expected missing mise error: %v", err)
		}
		if shims.IsNativeGHInstalled() || loadSettings(t).Native {
			t.Fatal("setup mutated state before validating mise")
		}
	})
}

func TestSetupMiseRefusesUnmanagedShimPath(t *testing.T) {
	requireNative(t)
	home := onboardingHome(t)
	fm := installFakeMise(t, home)
	path, err := shims.MiseGHPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	const content = "#!/bin/sh\necho mine\n"
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	if err := runSetupArgsErr("--native", "--no-shell-rc", "--mise"); err == nil {
		t.Fatal("replaced an unmanaged gh")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != content {
		t.Fatalf("unmanaged file changed: %q, %v", data, err)
	}
	if len(fm.calls(t)) != 0 || shims.IsNativeGHInstalled() {
		t.Fatal("setup mutated state before refusing")
	}
	// Disabling must not delete a file this tool does not own either.
	if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	runSetupArgs(t, "--mise=false")
	if data, err := os.ReadFile(path); err != nil || string(data) != content {
		t.Fatalf("disable removed an unmanaged file: %q, %v", data, err)
	}
}

// pathWith prepends dirs to the test PATH.
func pathWith(t *testing.T, dirs ...string) {
	t.Helper()
	t.Setenv("PATH", strings.Join(append(dirs, os.Getenv("PATH")), string(os.PathListSeparator)))
}

// shadowGH writes a real-looking gh under a mise-style install path and returns it.
func shadowGH(t *testing.T, home, rel string) string {
	t.Helper()
	dir := filepath.Join(home, rel)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "bin", "gh"), filepath.Join(dir, "gh")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "gh")
}

func nativeShimHome(t *testing.T) (home, shimDir string) {
	t.Helper()
	requireNative(t)
	home = onboardingHome(t)
	if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shim, err := shims.InstallNativeGH(exe)
	if err != nil {
		t.Fatal(err)
	}
	return home, filepath.Dir(shim)
}

func TestDoctorFlagsShadowedShim(t *testing.T) {
	home, shimDir := nativeShimHome(t)
	miseGH := shadowGH(t, home, filepath.Join("data", "mise", "installs", "gh", "latest", "bin"))
	otherGH := shadowGH(t, home, "elsewhere")

	pathWith(t, filepath.Dir(miseGH), shimDir)
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "WARN plain gh resolves to "+miseGH+", not the managed shim") || !strings.Contains(out, "gh autoaccount setup --mise") || strings.Contains(out, "external PATH integration not verified") {
		t.Fatalf("mise-installed gh not flagged with the mise hint:\n%s", out)
	}

	pathWith(t, filepath.Dir(otherGH), shimDir)
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "WARN plain gh resolves to "+otherGH) || !strings.Contains(out, "put "+shimDir+" before "+filepath.Dir(otherGH)+" on PATH") || strings.Contains(out, "setup --mise") {
		t.Fatalf("non-mise shadow needs PATH guidance:\n%s", out)
	}

	pathWith(t, shimDir)
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "OK   plain gh resolves to the managed shim") || strings.Contains(out, "WARN plain gh") {
		t.Fatalf("shim-first PATH flagged:\n%s", out)
	}

	// The gh-only shim counts as managed too.
	exe, _ := os.Executable()
	miseShim, err := shims.InstallMiseGH(exe)
	if err != nil {
		t.Fatal(err)
	}
	pathWith(t, filepath.Dir(miseShim), shimDir)
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "OK   plain gh resolves to the managed shim") || !strings.Contains(out, "path: "+miseShim) {
		t.Fatalf("gh-only shim not recognized:\n%s", out)
	}
}

func TestCheckGHOnPathCountsIssues(t *testing.T) {
	home, shimDir := nativeShimHome(t)
	fm := installFakeMise(t, home)
	shadow := shadowGH(t, home, "elsewhere")
	settings := config.Settings{Native: true}
	shim := filepath.Join(shimDir, "gh")
	check := func() (bool, int, string) {
		var verified bool
		var issues int
		out := captureOnboarding(t, func() error { verified, issues = checkGHOnPath(settings); return nil })
		return verified, issues, out
	}

	pathWith(t, shimDir)
	t.Setenv("MISE_FAKE_EXEC_GH", shim)
	if verified, issues, out := check(); !verified || issues != 0 || !strings.Contains(out, "OK   gh inside mise exec resolves to the managed shim") {
		t.Fatalf("healthy: %t %d\n%s", verified, issues, out)
	}
	if got := fm.calls(t); !reflect.DeepEqual(got, []string{"exec -- sh -c command -v gh"}) {
		t.Fatalf("mise exec must run once: %q", got)
	}

	t.Setenv("MISE_FAKE_EXEC_GH", shadow)
	if verified, issues, out := check(); !verified || issues != 1 || !strings.Contains(out, "WARN gh inside mise exec resolves to "+shadow) || strings.Contains(out, "WARN plain gh") {
		t.Fatalf("shadowed only inside mise: %t %d\n%s", verified, issues, out)
	}

	pathWith(t, filepath.Dir(shadow))
	if _, issues, _ := check(); issues != 2 {
		t.Fatalf("both checks should count: %d", issues)
	}

	touch(t, fm.fail)
	if _, issues, out := check(); issues != 1 || !strings.Contains(out, "INFO cannot check gh inside mise exec") {
		t.Fatalf("a failing mise exec is not a verdict: %d\n%s", issues, out)
	}

	t.Setenv("PATH", filepath.Join(home, "no-gh-here"))
	if verified, issues, out := check(); verified || issues != 0 || out != "" {
		t.Fatalf("no gh on PATH must stay unverified: %t %d %q", verified, issues, out)
	}
}

func TestDoctorChecksMiseShimWhenEnabled(t *testing.T) {
	home, shimDir := nativeShimHome(t)
	installFakeMise(t, home)
	pathWith(t, shimDir)
	if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true, Mise: true}); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "WARN mise gh-only shim missing, broken, or unmanaged") {
		t.Fatalf("missing gh-only shim not flagged:\n%s", out)
	}
	exe, _ := os.Executable()
	if _, err := shims.InstallMiseGH(exe); err != nil {
		t.Fatal(err)
	}
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "OK   mise gh-only shim installed") || strings.Contains(out, "WARN mise gh-only shim") {
		t.Fatalf("installed gh-only shim flagged:\n%s", out)
	}
}

func TestStatusReportsShadowedShim(t *testing.T) {
	home, shimDir := nativeShimHome(t)
	shadow := shadowGH(t, home, filepath.Join("data", "mise", "installs", "gh", "latest", "bin"))

	for _, tc := range []struct {
		name       string
		first      string
		wantShim   bool
		wantResult string
	}{
		{"shadowed", filepath.Dir(shadow), false, shadow},
		{"shim first", shimDir, true, filepath.Join(shimDir, "gh")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pathWith(t, tc.first)
			stdout, stderr, err := executeDiagnostics("status", "--dir", home, "--json")
			if err != nil || stderr != "" {
				t.Fatalf("status failed: %v %s", err, stderr)
			}
			var status statusDiagnostics
			decodeDiagnosticJSON(t, stdout, &status)
			if status.SchemaVersion != 1 || status.GHOnPath == nil || status.GHOnPath.Path != tc.wantResult || status.GHOnPath.NativeShim != tc.wantShim {
				t.Fatalf("gh_on_path: %+v\n%s", status.GHOnPath, stdout)
			}
			text, _, err := executeDiagnostics("status", "--dir", home)
			if err != nil {
				t.Fatal(err)
			}
			warned := strings.Contains(text, "WARN: gh on PATH is not the managed shim")
			if warned == tc.wantShim || (warned && !strings.Contains(text, "gh autoaccount setup --mise")) {
				t.Fatalf("status warning mismatch (warned=%t):\n%s", warned, text)
			}
		})
	}

	t.Setenv("PATH", filepath.Join(home, "no-gh-here"))
	stdout, _, err := executeDiagnostics("status", "--dir", home, "--json")
	if err != nil || strings.Contains(stdout, "gh_on_path") {
		t.Fatalf("gh_on_path must be omitted without gh on PATH: %v\n%s", err, stdout)
	}
}

// TestRealMiseRegistersGHOnlyDirFirst drives the real mise binary against a
// throwaway global config. Its only tool is a locally linked gh, standing in for
// mise's own gh, and mise runs offline, so nothing is installed or fetched.
func TestRealMiseRegistersGHOnlyDirFirst(t *testing.T) {
	requireNative(t)
	realMise, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("mise not installed")
	}
	home := onboardingHome(t)
	sandbox := t.TempDir()
	configFile := filepath.Join(sandbox, "config", "config.toml")
	// mise refuses to edit a global config file that does not exist yet.
	if err := os.MkdirAll(filepath.Dir(configFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, []byte("[tools]\ngh = \"9.9.9\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"MISE_GLOBAL_CONFIG_FILE":     configFile,
		"MISE_CONFIG_DIR":             filepath.Dir(configFile),
		"MISE_TRUSTED_CONFIG_PATHS":   filepath.Dir(configFile),
		"MISE_DATA_DIR":               filepath.Join(sandbox, "data"),
		"MISE_STATE_DIR":              filepath.Join(sandbox, "state"),
		"MISE_CACHE_DIR":              filepath.Join(sandbox, "cache"),
		"MISE_OFFLINE":                "1",
		"MISE_DISABLE_UPDATE_WARNING": "1",
		"MISE_YES":                    "1",
	} {
		t.Setenv(key, value)
	}
	// Neither the repository nor its parents may contribute project config.
	t.Chdir(home)
	miseExecutable = func() (string, error) { return realMise, nil }
	dir := miseDir(t)

	// Mise puts its tools' bin directories ahead of PATH, which is the shadowing
	// the gh-only directory has to beat; link a gh there without installing.
	linked := filepath.Join(sandbox, "linked-gh")
	if err := os.MkdirAll(filepath.Join(linked, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, "bin", "gh"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := runMise("link", "gh@9.9.9", linked); err != nil {
		t.Fatal(err)
	}
	toolGH := filepath.Join(sandbox, "data", "installs", "gh", "9.9.9", "bin", "gh")
	resolve := func() string {
		out, err := runMise("exec", "--", "sh", "-c", "command -v gh")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := resolve(); got != toolGH {
		t.Fatalf("baseline gh should be mise's tool %s, got %s", toolGH, got)
	}

	runSetupArgs(t, "--native", "--no-shell-rc", "--mise")
	runSetupArgs(t, "--migrate")
	data, err := os.ReadFile(configFile)
	if err != nil || strings.Count(string(data), dir) != 1 {
		t.Fatalf("registration missing or duplicated in %s: %v\n%s", configFile, err, data)
	}
	if got := resolve(); got != filepath.Join(dir, "gh") || !shims.IsManagedGHShim(got) {
		t.Fatalf("gh-only dir is not first inside mise: %s", got)
	}

	runSetupArgs(t, "--mise=false")
	if data, err := os.ReadFile(configFile); err != nil || strings.Contains(string(data), dir) {
		t.Fatalf("registration not removed: %v\n%s", err, data)
	}
	if got := resolve(); got != toolGH {
		t.Fatalf("mise's gh not restored after disabling: %s", got)
	}
}

func TestSetupMiseRefusesImpureDirectory(t *testing.T) {
	requireNative(t)
	home := onboardingHome(t)
	fm := installFakeMise(t, home)
	dir := miseDir(t)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "git")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	err := runSetupArgsErr("--native", "--no-shell-rc", "--mise")
	if err == nil || !strings.Contains(err.Error(), "gh-only directory") || !strings.Contains(err.Error(), `"git"`) {
		t.Fatalf("expected the foreign entry to be refused: %v", err)
	}
	if len(fm.calls(t)) != 0 || shims.IsNativeGHInstalled() || loadSettings(t).Mise {
		t.Fatal("setup mutated state before refusing")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("foreign file touched: %v", err)
	}
}

func TestShadowFixHintOnlyBlamesMiseForMisePaths(t *testing.T) {
	home := onboardingHome(t)
	installFakeMise(t, home) // mise is on PATH, which alone must not trigger the hint
	settings := config.Settings{Native: true}
	other := filepath.Join(home, "elsewhere", "gh")
	if hint := shadowFixHint(other, settings); strings.Contains(hint, "--mise") || !strings.Contains(hint, "on PATH") {
		t.Fatalf("non-mise path got %q", hint)
	}
	for _, path := range []string{
		"/home/u/.local/share/mise/installs/gh/1/bin/gh",
		"/home/u/.local/share/mise/shims/gh",
	} {
		if hint := shadowFixHint(path, settings); !strings.Contains(hint, "setup --mise") {
			t.Fatalf("%s got %q", path, hint)
		}
	}
	t.Setenv("MISE_DATA_DIR", filepath.Join(home, "custom-data"))
	if hint := shadowFixHint(filepath.Join(home, "custom-data", "installs", "gh", "gh"), settings); !strings.Contains(hint, "setup --mise") {
		t.Fatalf("MISE_DATA_DIR path got %q", hint)
	}
	if hint := shadowFixHint(other, config.Settings{Native: true, Mise: true}); !strings.Contains(hint, "setup --migrate") {
		t.Fatalf("enabled mise got %q", hint)
	}
}

func TestRunMiseIsBoundedWhenAChildHoldsThePipe(t *testing.T) {
	home := onboardingHome(t)
	script := filepath.Join(home, "mise-lingering")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 8 &\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	miseExecutable = func() (string, error) { return script, nil }
	start := time.Now()
	_, _ = runMise("exec")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("runMise waited %s for a lingering child", elapsed)
	}
}

func TestParseMisePath(t *testing.T) {
	for _, tc := range []struct {
		out     string
		want    []string
		wantErr bool
	}{
		{out: "", want: nil},
		{out: `[]`, want: nil},
		{out: `["/a"]`, want: []string{"/a"}},
		{out: `["/a", "/b c"]`, want: []string{"/a", "/b c"}},
		{out: `~/bin`, want: []string{"~/bin"}},
		{out: `["C:\x", "/b"]`, want: []string{`C:\x`, "/b"}},
		{out: `{ a = 1 }`, wantErr: true},
		{out: `["/a", 1]`, wantErr: true},
		{out: `[1]`, wantErr: true},
		{out: `["/a"`, wantErr: true},
	} {
		got, err := parseMisePath(tc.out)
		if (err != nil) != tc.wantErr || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("parseMisePath(%q) = %q, %v", tc.out, got, err)
		}
		if err != nil && !strings.Contains(err.Error(), "env._.path") {
			t.Errorf("error must name the key: %v", err)
		}
	}
}

func TestSetupMiseHandlesScalarAndRejectsOddPathShapes(t *testing.T) {
	requireNative(t)
	t.Run("scalar", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		t.Setenv("MISE_FAKE_GET_RAW", "~/bin")
		runSetupArgs(t, "--native", "--no-shell-rc", "--mise")
		dir := miseDir(t)
		if got := fm.calls(t); !reflect.DeepEqual(got, []string{"config set -g --append env._.path " + dir}) || !loadSettings(t).Mise {
			t.Fatalf("scalar value blocked enable: %q", got)
		}
	})
	t.Run("table fails closed", func(t *testing.T) {
		home := onboardingHome(t)
		fm := installFakeMise(t, home)
		t.Setenv("MISE_FAKE_GET_RAW", "{ a = 1 }")
		err := runSetupArgsErr("--native", "--no-shell-rc", "--mise")
		if err == nil || !strings.Contains(err.Error(), "env._.path") {
			t.Fatalf("expected an error naming the key: %v", err)
		}
		if len(fm.calls(t)) != 0 || shims.IsNativeGHInstalled() || loadSettings(t).Mise {
			t.Fatal("state changed despite the unreadable config")
		}
	})
}

func TestSetupMiseRefusesSymlinkedLayout(t *testing.T) {
	requireNative(t)
	for _, which := range []string{"dir", "parent"} {
		t.Run(which, func(t *testing.T) {
			home := onboardingHome(t)
			fm := installFakeMise(t, home)
			dir := miseDir(t)
			real := filepath.Join(home, "real-layout")
			if err := os.MkdirAll(filepath.Join(real, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			link, target := dir, filepath.Join(real, "bin")
			if which == "parent" {
				link, target = filepath.Dir(dir), real
			}
			if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"--native", "--no-shell-rc", "--mise"}, {"--mise=false"}} {
				err := runSetupArgsErr(args...)
				if err == nil || !strings.Contains(err.Error(), "symlink") {
					t.Fatalf("%v: expected symlink refusal: %v", args, err)
				}
			}
			if len(fm.calls(t)) != 0 || shims.IsNativeGHInstalled() {
				t.Fatal("state changed despite the refusal")
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("symlinked layout replaced: %v", err)
			}
		})
	}
}
