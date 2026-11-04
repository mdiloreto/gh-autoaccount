package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

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
}

// A shim directory that is on PATH only after another gh (Homebrew, a distro
// package) does not route plain gh, so setup must still add the PATH block.
func TestFreshSetupAddsPathBlockWhenShimDirIsShadowed(t *testing.T) {
	requireNativeSupport(t)
	for _, tc := range []struct {
		name                 string
		shimFirst, symlinked bool
		wantBlock            bool
	}{
		{"shim dir after another gh", false, false, true},
		{"shim dir already first", true, false, false},
		{"shim dir first through a symlinked spelling", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := onboardingHome(t)
			shimDir := filepath.Join(home, ".local", "bin")
			if tc.symlinked {
				if err := os.MkdirAll(shimDir, 0700); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(t.TempDir(), "home-alias")
				if err := os.Symlink(home, alias); err != nil {
					t.Fatal(err)
				}
				shimDir = filepath.Join(alias, ".local", "bin")
			}
			if tc.shimFirst {
				t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			} else {
				t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+shimDir)
			}
			captureOnboarding(t, NewSetupCmd().Execute)
			// An RC file nobody else wrote exists only when setup added the block.
			rc, err := os.ReadFile(filepath.Join(home, ".bashrc"))
			if err != nil && !(os.IsNotExist(err) && !tc.wantBlock) {
				t.Fatal(err)
			}
			if got := strings.Contains(string(rc), "gh-autoprofile-shims:start"); got != tc.wantBlock {
				t.Fatalf("PATH block written = %v, want %v:\n%s", got, tc.wantBlock, rc)
			}
		})
	}
}

// Login shells (bash -l, zsh -l, macOS terminals, many agent tools) skip the
// interactive RC file, so the PATH block must also reach the login profile.
func TestSetupWritesPathBlockToLoginProfile(t *testing.T) {
	requireNativeSupport(t)
	const block = "gh-autoprofile-shims:start"
	for _, tc := range []struct {
		name, shell string
		seed        map[string]string // files present before setup
		args        []string
		want        []string // files that must carry the block
		absent      []string // files that must not be created
	}{
		{name: "bash without profiles", shell: "/bin/bash", want: []string{".bashrc", ".profile"}},
		{name: "bash with bash_profile", shell: "/bin/bash", seed: map[string]string{".bash_profile": "# mine\n"}, want: []string{".bashrc", ".bash_profile"}, absent: []string{".profile"}},
		{name: "bash skips a dangling bash_profile", shell: "/bin/bash", seed: map[string]string{".bash_profile": "dangling", ".profile": "# mine\n"}, want: []string{".bashrc", ".profile"}},
		{name: "zsh", shell: "/bin/zsh", want: []string{".zshrc", ".zprofile"}},
		{name: "existing rc block gains profile on migrate", shell: "/bin/bash", args: []string{"--native", "--migrate"},
			seed: map[string]string{".bashrc": "# gh-autoprofile-shims:start\nexport PATH='/old':\"$PATH\"\n# gh-autoprofile-shims:end\n"},
			want: []string{".bashrc", ".profile"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := onboardingHome(t)
			t.Setenv("SHELL", tc.shell)
			for name, content := range tc.seed {
				if content == "dangling" {
					if err := os.Symlink(filepath.Join(home, "missing"), filepath.Join(home, name)); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := NewSetupCmd()
			command.SetArgs(tc.args)
			captureOnboarding(t, command.Execute)
			for _, name := range tc.want {
				data, err := os.ReadFile(filepath.Join(home, name))
				if err != nil || strings.Count(string(data), block) != 1 {
					t.Fatalf("%s lacks exactly one PATH block (%v):\n%s", name, err, data)
				}
			}
			for _, name := range tc.absent {
				if _, err := os.Stat(filepath.Join(home, name)); !os.IsNotExist(err) {
					t.Fatalf("%s was created: %v", name, err)
				}
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
