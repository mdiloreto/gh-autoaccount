package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/legacy"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

const (
	legacyLibHeader  = "# gh-autoprofile direnv shell library"
	legacyHookHeader = "# gh-autoprofile shell hook"
	rcHookBlock      = "# gh-autoprofile-hook:start\nsource \"$HOME/.config/gh-autoprofile/hook.sh\"\n# gh-autoprofile-hook:end\n"
	rcPathBlock      = "# gh-autoprofile-shims:start\nexport PATH='/old/shims':\"$PATH\"\n# gh-autoprofile-shims:end\n"
	rcBefore         = "# mine\r\nexport A=1\n"
	rcAfter          = "export B=2\n" + rcPathBlock
)

// betaHome lays out what the last release with the direnv modes left behind.
type betaHome struct {
	home, lib, hook, rc                       string
	wrapperDir, exportDir, malformedDir       string
	wrapperEnvrc, exportEnvrc, malformedEnvrc string
}

const (
	exportCustom    = "export CUSTOM=1\n"
	malformedEnvrc  = "export KEEP=1\n# gh-autoprofile:start\nuse_gh_autoprofile alice\n"
	managedEnvrcFmt = "# gh-autoprofile:start\nuse_gh_autoprofile %s\n# gh-autoprofile:end\n"
)

func seedBetaHome(t *testing.T, includeMalformed bool) betaHome {
	t.Helper()
	b := betaHome{home: onboardingHome(t)}
	var err error
	if b.lib, err = legacy.ShellLibPath(); err != nil {
		t.Fatal(err)
	}
	if b.hook, err = legacy.ShellHookPath(); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		b.lib:  "#!/bin/sh\n" + legacyLibHeader + "\n",
		b.hook: "#!/bin/sh\n" + legacyHookHeader + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	b.rc = filepath.Join(b.home, ".bashrc")
	if err := os.WriteFile(b.rc, []byte(rcBefore+rcHookBlock+rcAfter), 0644); err != nil {
		t.Fatal(err)
	}
	mkdir := func(name string) string {
		dir := filepath.Join(b.home, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	write := func(dir, content string) string {
		path := filepath.Join(dir, ".envrc")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	b.wrapperDir, b.exportDir = mkdir("wrapper"), mkdir("export")
	b.wrapperEnvrc = write(b.wrapperDir, strings.Replace(managedEnvrcFmt, "%s", "alice", 1))
	b.exportEnvrc = write(b.exportDir, exportCustom+strings.Replace(managedEnvrcFmt, "%s", "alice", 1))
	pins := []config.Pin{
		{Dir: b.wrapperDir, User: "alice", Mode: config.ModeWrapper},
		{Dir: b.exportDir, User: "alice", Mode: config.ModeExport},
	}
	if includeMalformed {
		b.malformedDir = mkdir("malformed")
		b.malformedEnvrc = write(b.malformedDir, malformedEnvrc)
		pins = append(pins, config.Pin{Dir: b.malformedDir, User: "alice", Mode: config.ModeWrapper})
	}
	if err := config.SavePins(&config.PinRegistry{Pins: pins}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{}); err != nil {
		t.Fatal(err)
	}
	return b
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func gone(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Lstat(path)
	return os.IsNotExist(err)
}

func TestSetupRemovesWhatTheDirenvModesLeft(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	b := seedBetaHome(t, true)
	log := filepath.Join(b.home, "direnv-calls")
	t.Setenv("TEST_DIRENV_LOG", log)
	if err := os.WriteFile(filepath.Join(b.home, "bin", "direnv"), []byte("#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> \"$TEST_DIRENV_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}

	out := captureOnboarding(t, NewSetupCmd().Execute)

	if !gone(t, b.lib) || !gone(t, b.hook) {
		t.Fatalf("library or hook left behind: lib gone=%t hook gone=%t", gone(t, b.lib), gone(t, b.hook))
	}
	if got := readFile(t, b.rc); got != rcBefore+rcAfter {
		t.Fatalf("RC changed beyond the hook block: %q", got)
	}
	registry, err := config.LoadPins()
	if err != nil || len(registry.Pins) != 3 {
		t.Fatalf("pins: %+v %v", registry, err)
	}
	for _, pin := range registry.Pins {
		if pin.Mode != config.ModeWrapper {
			t.Fatalf("pin %s still has mode %q", pin.Dir, pin.Mode)
		}
	}
	if !gone(t, b.wrapperEnvrc) {
		t.Fatal("managed-only .envrc not removed")
	}
	if got := readFile(t, b.exportEnvrc); got != exportCustom {
		t.Fatalf("custom .envrc bytes changed: %q", got)
	}
	if got := readFile(t, b.malformedEnvrc); got != malformedEnvrc {
		t.Fatalf("malformed .envrc touched: %q", got)
	}
	for _, want := range []string{"Removing legacy files", "REVIEW", "Review required: " + b.malformedEnvrc} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Review required: "+b.exportEnvrc) || strings.Contains(out, "Remove the line that sources") {
		t.Fatalf("output asks for review of something cleaned:\n%s", out)
	}
	calls := readFile(t, log)
	for _, path := range []string{b.wrapperEnvrc, b.exportEnvrc} {
		if !strings.Contains(calls, "deny "+path+"\n") {
			t.Fatalf("authorization of %s not revoked: %q", path, calls)
		}
	}
	if strings.Contains(calls, b.malformedEnvrc) {
		t.Fatalf("malformed .envrc revoked before it could be cleaned: %q", calls)
	}
	if settings, err := config.LoadSettings(); err != nil || !settings.Native {
		t.Fatalf("settings: %+v %v", settings, err)
	}

	// A second run has nothing left to remove and must not touch what it kept.
	again := captureOnboarding(t, NewSetupCmd().Execute)
	if strings.Contains(again, "Removed ") || !strings.Contains(again, "Review required: "+b.malformedEnvrc) {
		t.Fatalf("second run output:\n%s", again)
	}
	if got := readFile(t, b.exportEnvrc); got != exportCustom {
		t.Fatalf("custom .envrc bytes changed on rerun: %q", got)
	}
	if got := readFile(t, b.rc); got != rcBefore+rcAfter {
		t.Fatalf("RC changed on rerun: %q", got)
	}
}

// Older setup told users to add the source line by hand when it could not edit
// the RC file; such a line has no managed markers.
func TestSetupKeepsHookSourcedOutsideManagedBlock(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	b := seedBetaHome(t, false)
	zshenv := filepath.Join(b.home, ".zshenv")
	manual := "source \"" + b.hook + "\"\n"
	if err := os.WriteFile(zshenv, []byte(manual), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_DIRENV_LOG", filepath.Join(b.home, "direnv-calls"))
	if err := os.WriteFile(filepath.Join(b.home, "bin", "direnv"), []byte("#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> \"$TEST_DIRENV_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}

	out := captureOnboarding(t, NewSetupCmd().Execute)

	if gone(t, b.hook) {
		t.Fatal("hook deleted while .zshenv still sources it")
	}
	if got := readFile(t, zshenv); got != manual {
		t.Fatalf("unmanaged startup file edited: %q", got)
	}
	if got := readFile(t, b.rc); got != rcBefore+rcAfter {
		t.Fatalf("managed RC block not removed: %q", got)
	}
	for _, want := range []string{"Remove the line that sources " + b.hook, "It is referenced from: " + zshenv} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}

// A modular shell config outside the home startup files can still source the
// hook; RC-managed setup must keep it and say where it is referenced, and doctor
// must point at the manual step rather than another migrate.
func TestSetupKeepsHookSourcedFromModularConfig(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	b := seedBetaHome(t, false)
	tool := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "shell", "tools", "gh-autoprofile.zsh")
	if err := os.MkdirAll(filepath.Dir(tool), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("[ -f \"$HOME/.config/gh-autoprofile/hook.sh\" ] && source \"$HOME/.config/gh-autoprofile/hook.sh\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_DIRENV_LOG", filepath.Join(b.home, "direnv-calls"))
	if err := os.WriteFile(filepath.Join(b.home, "bin", "direnv"), []byte("#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> \"$TEST_DIRENV_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}

	out := captureOnboarding(t, NewSetupCmd().Execute)
	if gone(t, b.hook) {
		t.Fatal("hook deleted while a modular shell config still sources it")
	}
	if !strings.Contains(out, "It is referenced from: ") || !strings.Contains(out, tool) {
		t.Fatalf("setup does not name the referencing file:\n%s", out)
	}

	// With the shim first on PATH, the kept hook is the only remaining issue.
	t.Setenv("PATH", filepath.Join(b.home, ".local", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	doctor := captureOnboarding(t, NewDoctorCmd().Execute)
	for _, want := range []string{"is kept because your shell config may still source it", "referenced from: " + tool} {
		if !strings.Contains(doctor, want) {
			t.Fatalf("doctor lacks %q:\n%s", want, doctor)
		}
	}
	if strings.Contains(doctor, "WARN files left from the retired direnv modes") || !strings.Contains(doctor, "The remaining issues need the manual steps shown above.") {
		t.Fatalf("a kept hook is a manual step, not something migrate removes:\n%s", doctor)
	}
}

func TestSetupExternalShellKeepsHookAndRC(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	b := seedBetaHome(t, false)
	original := readFile(t, b.rc)

	out := captureOnboarding(t, func() error {
		command := NewSetupCmd()
		command.SetArgs([]string{"--no-shell-rc"})
		return command.Execute()
	})

	if got := readFile(t, b.rc); got != original {
		t.Fatalf("external-shell RC edited: %q", got)
	}
	if gone(t, b.hook) {
		t.Fatal("hook deleted while the user's shell config may still source it")
	}
	if !gone(t, b.lib) || !gone(t, b.wrapperEnvrc) {
		t.Fatal("files that no shell sources were not removed")
	}
	if !strings.Contains(out, "Remove the line that sources "+b.hook) {
		t.Fatalf("missing manual unhook instruction:\n%s", out)
	}
}

func TestRetiredModesFailWithReplacement(t *testing.T) {
	home := onboardingHome(t)
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	pinsPath, err := config.PinsFilePath()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("pin --export-token", func(t *testing.T) {
		command := NewPinCmd()
		command.SetArgs([]string{"alice", "--dir", repo, "--export-token"})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "gh autoaccount exec") {
			t.Fatalf("missing replacement text: %v", err)
		}
		if !gone(t, pinsPath) || !gone(t, filepath.Join(repo, ".envrc")) {
			t.Fatal("refused pin left state behind")
		}
	})

	t.Run("setup --native=false", func(t *testing.T) {
		command := NewSetupCmd()
		command.SetArgs([]string{"--native=false", "--no-shell-rc"})
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), "gh autoaccount exec") {
			t.Fatalf("missing replacement text: %v", err)
		}
		if shims.IsNativeGHInstalled() || !gone(t, pinsPath) {
			t.Fatal("refused setup changed state")
		}
		if settings, err := config.LoadSettings(); err != nil || settings.Native {
			t.Fatalf("refused setup persisted settings: %+v %v", settings, err)
		}
	})
}

func TestDoctorReportsLeftoversAndPassesOnceGone(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	b := seedBetaHome(t, false)

	out := captureOnboarding(t, NewDoctorCmd().Execute)
	for _, want := range []string{
		"WARN files left from the retired direnv modes",
		`pin for ` + b.exportDir + ` is not migrated (mode "export")`,
		filepath.Join(b.wrapperDir, ".envrc") + " has a retired managed block",
		b.lib,
		b.hook,
		"referenced from: " + b.rc,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Doctor check passed") {
		t.Fatalf("doctor passed with leftovers:\n%s", out)
	}

	captureOnboarding(t, NewSetupCmd().Execute)

	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "OK   no files left from the retired direnv modes") || strings.Contains(out, "WARN files left") {
		t.Fatalf("doctor still reports leftovers:\n%s", out)
	}
}

func TestUpgradeWarningFollowsLeftovers(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	b := seedBetaHome(t, false)
	warn := func() string {
		root := NewRootCmd()
		var stderr strings.Builder
		root.SetErr(&stderr)
		warnUpgradeDrift(root)
		return stderr.String()
	}
	if !strings.Contains(warn(), "setup --migrate") {
		t.Fatal("no upgrade warning with leftovers")
	}
	captureOnboarding(t, NewSetupCmd().Execute)
	if got := warn(); got != "" {
		t.Fatalf("warning after cleanup: %q", got)
	}
	// In external-shell mode the kept hook is the user's to unhook, so it
	// alone must not keep warning on every command.
	if err := os.WriteFile(b.hook, []byte("#!/bin/sh\n"+legacyHookHeader+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	if got := warn(); got != "" {
		t.Fatalf("warning for a hook kept in external-shell mode: %q", got)
	}
	if err := config.SaveSettings(config.Settings{Native: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn(), "setup --migrate") {
		t.Fatal("no warning for a leftover hook outside external-shell mode")
	}
}
