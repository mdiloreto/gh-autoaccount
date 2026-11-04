package cmd

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

func captureStderr(t *testing.T, run func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	result := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		result <- string(data)
	}()
	run()
	w.Close()
	output := <-result
	r.Close()
	return output
}

func requireNativeSupport(t *testing.T) {
	t.Helper()
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
}

func TestFreshSetupDefaultsToNative(t *testing.T) {
	requireNativeSupport(t)
	onboardingHome(t)
	command := NewSetupCmd()
	var stderr bytes.Buffer
	command.SetErr(&stderr)
	command.SetArgs([]string{"--no-shell-rc"})
	captureOnboarding(t, command.Execute)
	settings, err := config.LoadSettings()
	if err != nil || !settings.Native {
		t.Fatalf("fresh setup is not native: %+v %v", settings, err)
	}
	if !shims.IsNativeGHInstalled() || !gitcred.IsInstalled() || !gitcred.CheckConfigured() {
		t.Fatal("native proxy or Git helper missing after fresh setup")
	}
	if strings.Contains(stderr.String(), "deprecated") {
		t.Fatalf("native setup printed a deprecation notice: %s", stderr.String())
	}
}

func TestFreshSetupIgnoresShimDirOnProcessPath(t *testing.T) {
	requireNativeSupport(t)
	home := onboardingHome(t)
	t.Setenv("PATH", filepath.Join(home, ".local", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	command := NewSetupCmd()
	command.SetArgs([]string{"--no-shell-rc"})
	captureOnboarding(t, command.Execute)
	if settings, err := config.LoadSettings(); err != nil || !settings.Native {
		t.Fatalf("shim dir on PATH made a fresh install legacy: %+v %v", settings, err)
	}
}

// A shim directory that is on PATH only after another gh (Homebrew, a distro
// package) does not route plain gh, so setup must still add the PATH block.
func TestFreshSetupAddsPathBlockWhenShimDirIsShadowed(t *testing.T) {
	requireNativeSupport(t)
	for _, tc := range []struct {
		name      string
		shimFirst bool
		wantBlock bool
	}{
		{"shim dir after another gh", false, true},
		{"shim dir already first", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := onboardingHome(t)
			shimDir := filepath.Join(home, ".local", "bin")
			if tc.shimFirst {
				t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			} else {
				t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+shimDir)
			}
			captureOnboarding(t, NewSetupCmd().Execute)
			rc, err := os.ReadFile(filepath.Join(home, ".bashrc"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(rc), "gh-autoprofile-shims:start"); got != tc.wantBlock {
				t.Fatalf("PATH block written = %v, want %v:\n%s", got, tc.wantBlock, rc)
			}
		})
	}
}

func TestLegacyNoticeSuppressedWhereNativeUnsupported(t *testing.T) {
	old := nativeGHSupported
	nativeGHSupported = func() bool { return false }
	t.Cleanup(func() { nativeGHSupported = old })
	home := onboardingHome(t)
	if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	var setupErr bytes.Buffer
	setup := NewSetupCmd()
	setup.SetErr(&setupErr)
	setup.SetArgs([]string{"--no-shell-rc"})
	captureOnboarding(t, setup.Execute)
	if settings, _ := config.LoadSettings(); settings.Native {
		t.Fatal("unsupported platform defaulted to native")
	}
	pinErr := captureStderr(t, func() {
		captureOnboarding(t, func() error { return runPin("alice", repo, "", "", "", false) })
	})
	doctor := captureOnboarding(t, NewDoctorCmd().Execute)
	status := captureOnboarding(t, NewStatusCmd().Execute)
	for name, out := range map[string]string{"setup": setupErr.String(), "pin": pinErr, "doctor": doctor, "status": status} {
		if strings.Contains(out, "deprecated") || strings.Contains(out, "setup --native") {
			t.Fatalf("%s told the user to run an unsupported command: %s", name, out)
		}
	}
}

func TestExistingLegacyInstallStaysLegacy(t *testing.T) {
	requireNativeSupport(t)
	for _, tc := range []struct {
		name string
		seed func(t *testing.T, home string)
	}{
		{"managed RC marker without native key", func(t *testing.T, home string) {
			hook, err := direnvlib.ShellHookPath()
			if err != nil {
				t.Fatal(err)
			}
			if err := direnvlib.InjectHookSource(filepath.Join(home, ".bashrc"), hook); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSettings(config.Settings{}); err != nil {
				t.Fatal(err)
			}
		}},
		{"explicit native=false", func(t *testing.T, home string) {
			command := NewSetupCmd()
			command.SetArgs([]string{"--native=false", "--no-shell-rc"})
			captureOnboarding(t, command.Execute)
		}},
		{"managed envrc pin", func(t *testing.T, home string) {
			dir := filepath.Join(home, "work")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			pin := config.Pin{Dir: dir, User: "alice"}
			if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{pin}}); err != nil {
				t.Fatal(err)
			}
			if err := direnvlib.WriteEnvrc(pin); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := onboardingHome(t)
			tc.seed(t, home)
			for _, mode := range []string{"setup", "setup --migrate", "doctor --fix"} {
				var stderr bytes.Buffer
				command := NewSetupCmd()
				switch mode {
				case "setup":
				case "setup --migrate":
					command.SetArgs([]string{"--migrate"})
				case "doctor --fix":
					command = NewDoctorCmd()
					command.SetArgs([]string{"--fix"})
				}
				command.SetErr(&stderr)
				captureOnboarding(t, command.Execute)
				settings, err := config.LoadSettings()
				if err != nil || settings.Native || shims.IsNativeGHInstalled() {
					t.Fatalf("%s flipped a legacy install: %+v %v", mode, settings, err)
				}
				if !strings.Contains(stderr.String(), "gh autoaccount setup --native") {
					t.Fatalf("%s missing deprecation warning: %q", mode, stderr.String())
				}
			}
		})
	}
}

func TestLegacyDeprecationInDoctorAndStatus(t *testing.T) {
	for _, native := range []bool{false, true} {
		onboardingHome(t)
		if err := config.SaveSettings(config.Settings{Native: native}); err != nil {
			t.Fatal(err)
		}
		doctor := captureOnboarding(t, NewDoctorCmd().Execute)
		status := captureOnboarding(t, NewStatusCmd().Execute)
		if native {
			if strings.Contains(doctor, "deprecated") || strings.Contains(status, "deprecated") {
				t.Fatalf("native mode reported a deprecation:\n%s\n%s", doctor, status)
			}
			continue
		}
		if !strings.Contains(doctor, "WARN legacy wrapper routing is deprecated") || !strings.Contains(doctor, "gh autoaccount setup --native") || strings.Contains(doctor, "Doctor check passed") {
			t.Fatalf("doctor missing legacy warning:\n%s", doctor)
		}
		if !strings.Contains(status, "WARN: legacy wrapper routing is deprecated") || !strings.Contains(status, "gh autoaccount setup --native") {
			t.Fatalf("status missing legacy warning:\n%s", status)
		}
	}
}

func TestPinDeprecationNotices(t *testing.T) {
	for _, tc := range []struct {
		name        string
		native      bool
		export      bool
		wantLegacy  bool
		wantExports bool
	}{
		{name: "legacy wrapper", wantLegacy: true},
		{name: "legacy export", export: true, wantExports: true},
		{name: "native wrapper", native: true},
		{name: "native export", native: true, export: true, wantExports: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := onboardingHome(t)
			if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSettings(config.Settings{Native: tc.native}); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(home, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			stderr := captureStderr(t, func() {
				captureOnboarding(t, func() error { return runPin("alice", repo, "", "", "", tc.export) })
			})
			if got := strings.Contains(stderr, legacyDeprecation); got != tc.wantLegacy {
				t.Fatalf("legacy notice = %t, want %t: %q", got, tc.wantLegacy, stderr)
			}
			if got := strings.Contains(stderr, "gh autoaccount exec --"); got != tc.wantExports {
				t.Fatalf("export notice = %t, want %t: %q", got, tc.wantExports, stderr)
			}
			if strings.Count(stderr, "deprecated") > 1 {
				t.Fatalf("expected one notice, got %q", stderr)
			}
		})
	}
}

// The reported failure: a non-interactive shell never runs a prompt hook, so a
// pin only helps if the PATH-level proxy routes plain gh. This drives the real
// binary through setup, pin and a bare `bash -c` with only setup's PATH.
func TestFreshSetupRoutesNonInteractiveGH(t *testing.T) {
	requireNativeSupport(t)
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires Unix")
	}
	for _, tool := range []string{"bash", "git"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(err)
		}
	}
	root := t.TempDir()
	binary := filepath.Join(root, "gh-autoaccount")
	build := exec.Command("go", "build", "-o", binary, "./cmd/gh-autoaccount")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	home := filepath.Join(root, "home")
	fakeBin := filepath.Join(root, "fake-bin")
	pinned := filepath.Join(root, "pinned")
	unpinned := filepath.Join(root, "unpinned")
	for _, dir := range []string{home, fakeBin, pinned, unpinned} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	callLog := filepath.Join(root, "gh-calls")
	fakeGH := `#!/bin/sh
case "$*" in
  --version) printf 'gh version 2.50.0\n' ;;
  'auth status') printf 'github.com\n  Logged in to github.com account alice (keyring)\n  Logged in to github.com account carol (keyring)\n' ;;
  'auth token --user alice --hostname github.com') printf 'alice-token\n' ;;
  'api user') printf 'api user as token=%s\n' "${GH_TOKEN-unset}" >> "$FAKE_GH_LOG" ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(fakeGH), 0700); err != nil {
		t.Fatal(err)
	}
	basePath := "/usr/bin:/bin"
	env := []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(home, ".gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"SHELL=/bin/bash", "FAKE_GH_LOG=" + callLog,
		"PATH=" + fakeBin + ":" + basePath,
	}
	run := func(dir string, name string, args ...string) string {
		t.Helper()
		command := exec.Command(name, args...)
		command.Env, command.Dir = env, dir
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}
	run(root, binary, "setup", "--no-shell-rc")
	run(root, binary, "pin", "alice", "--dir", pinned)

	// Setup adds the managed shim directory to PATH through the user's shell
	// configuration; reproduce that effect for a shell that reads no RC file.
	shimPath := filepath.Join(home, ".local", "bin") + ":" + fakeBin + ":" + basePath
	env[len(env)-1] = "PATH=" + shimPath
	for _, tc := range []struct{ dir, want string }{
		{pinned, "api user as token=alice-token\n"},
		{unpinned, "api user as token=unset\n"},
	} {
		if err := os.RemoveAll(callLog); err != nil {
			t.Fatal(err)
		}
		run(root, "bash", "-c", `cd "$1" && gh api user`, "bash", tc.dir)
		got, err := os.ReadFile(callLog)
		if err != nil || string(got) != tc.want {
			t.Fatalf("%s: real gh saw %q, want %q (%v)", tc.dir, got, tc.want, err)
		}
	}
}
