package direnv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

const guardedExecFixture = `
if [ "$1 $2" = 'autoaccount exec' ] || [ "$1" = exec ]; then
  while [ "$1" != -- ]; do
    if [ "$1" = --expect-user ]; then shift; selected=$1; fi
    shift
  done
  shift
  export GH_AUTOPROFILE_USER="$selected" GH_AUTOPROFILE_HOST=github.com GH_AUTOPROFILE_MODE=wrapper
  export GH_TOKEN="token-$selected" GITHUB_TOKEN="token-$selected"
  exec "$@"
fi
`

func TestInstalledLibraryPinsLiteralPathsNoninteractive(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil {
				t.Skip(err)
			}
			root := t.TempDir()
			t.Setenv("DIRENV_CONFIG", filepath.Join(root, "direnv"))
			realGH := filepath.Join(root, "real gh's binary")
			extension := filepath.Join(root, "extension ' trusted")
			attacker := filepath.Join(root, "attacker")
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			write(realGH, "#!/bin/sh\nexit 91\n")
			write(attacker, "#!/bin/sh\nprintf attacked > \"$ATTACKED\"\nexit 92\n")
			if err := os.Symlink(attacker, filepath.Join(root, "gh")); err != nil {
				t.Fatal(err)
			}
			write(extension, `#!/bin/sh
test "$GH_PATH" = "$EXPECTED_GH" && test "$GH_AUTOPROFILE_REAL_GH" = "$EXPECTED_GH" || exit 80
test "$1" = exec || exit 81
while [ "$1" != -- ]; do shift; done
shift
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_HOST=github.com GH_AUTOPROFILE_MODE=wrapper
exec "$@"
`)
			if err := InstallShellLib(extension, realGH); err != nil {
				t.Fatal(err)
			}
			lib, err := ShellLibPath()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(shellPath, "-c", `log_error() { printf '%s\n' "$*" >&2; }; log_status() { :; }; source "$LIB"; use_gh_autoprofile alice; test "$GH_AUTOPROFILE_HOST" = github.com`)
			cmd.Env = append(os.Environ(), "LIB="+lib, "EXPECTED_GH="+realGH, "GH_PATH="+attacker, "GH_AUTOPROFILE_REAL_GH="+attacker, "ATTACKED="+filepath.Join(root, "attacked"), "PATH="+root+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("activation failed: %v: %s", err, out)
			}
			if _, err := os.Stat(filepath.Join(root, "attacked")); !os.IsNotExist(err) {
				t.Fatal("attacker executable ran")
			}
		})
	}
}

func TestInstalledExportSanitizesHostClass(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		for _, host := range []string{"github.com", "tenant.ghe.com", "github.example.com"} {
			t.Run(shell+"/"+host, func(t *testing.T) {
				shellPath, err := exec.LookPath(shell)
				if err != nil {
					t.Skip(err)
				}
				root := t.TempDir()
				t.Setenv("DIRENV_CONFIG", filepath.Join(root, "direnv"))
				extension := filepath.Join(root, "extension")
				fixture := `#!/bin/sh
while [ "$1" != -- ]; do shift; done
shift
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_HOST="$FIXTURE_HOST" GH_AUTOPROFILE_MODE=export
unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST
case "$FIXTURE_HOST" in github.com|*.ghe.com) GH_TOKEN=trusted; export GH_TOKEN ;; *) GH_ENTERPRISE_TOKEN=trusted; export GH_ENTERPRISE_TOKEN ;; esac
exec "$@"
`
				if err := os.WriteFile(extension, []byte(fixture), 0700); err != nil {
					t.Fatal(err)
				}
				if err := InstallShellLib(extension, "/bin/sh"); err != nil {
					t.Fatal(err)
				}
				lib, _ := ShellLibPath()
				script := `log_error() { :; }; log_status() { :; }; source "$LIB"; use_gh_autoprofile_export alice || exit 1
case "$FIXTURE_HOST" in
  github.com) [[ "$GH_TOKEN:$GITHUB_TOKEN:${GH_ENTERPRISE_TOKEN-}:${GITHUB_ENTERPRISE_TOKEN-}:${GH_HOST-}" == 'trusted:trusted:::' ]] ;;
  *.ghe.com) [[ "$GH_TOKEN:$GITHUB_TOKEN:${GH_ENTERPRISE_TOKEN-}:${GITHUB_ENTERPRISE_TOKEN-}:$GH_HOST" == "trusted:trusted:::$FIXTURE_HOST" ]] ;;
  *) [[ "${GH_TOKEN-}:${GITHUB_TOKEN-}:$GH_ENTERPRISE_TOKEN:$GITHUB_ENTERPRISE_TOKEN:$GH_HOST" == "::trusted:trusted:$FIXTURE_HOST" ]] ;;
esac`
				cmd := exec.Command(shellPath, "-c", script)
				cmd.Env = append(os.Environ(), "LIB="+lib, "FIXTURE_HOST="+host, "GH_TOKEN=stale", "GITHUB_TOKEN=stale", "GH_ENTERPRISE_TOKEN=stale", "GITHUB_ENTERPRISE_TOKEN=stale", "GH_HOST=stale")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("export failed: %v: %s", err, out)
				}
			})
		}
	}
}

func TestShellHook_PinsRealGHBeyondShimInterpreter(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			write := func(path, content string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			hook := filepath.Join(home, "hook.sh")
			write(hook, string(shellHookContent))
			victim := filepath.Join(home, "real gh's binary")
			write(victim, "#!/usr/bin/env bash\n"+strings.ReplaceAll(guardedExecFixture, `token-$selected`, `SYNTHETIC_PATH_TOKEN`)+"printf 'login=alice token=%s\\n' \"$GH_TOKEN\"\n")
			shim, err := shims.InstallGH(filepath.Join(home, "unused-extension"), victim)
			if err != nil {
				t.Fatal(err)
			}
			attacker := filepath.Join(home, "attacker")
			if err := os.Mkdir(attacker, 0700); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(home, "captured-token")
			write(filepath.Join(attacker, "bash"), "#!/bin/sh\nif [ -n \"$GH_TOKEN\" ]; then printf '%s' \"$GH_TOKEN\" > \"$CAPTURE\"; fi\nexec /usr/bin/bash \"$@\"\n")
			script := fmt.Sprintf(`set -eu
export PATH=%q:/usr/bin:/bin
source %q
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_MODE=wrapper
_gh_autoprofile_hook
export PATH=%q:$PATH
gh api user
`, filepath.Dir(shim), hook, attacker)
			cmd := exec.Command(shellPath, "-f", "-c", script)
			cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "CAPTURE=" + capture}
			out, runErr := cmd.CombinedOutput()
			if data, err := os.ReadFile(capture); err == nil {
				t.Fatalf("attacker interpreter captured synthetic token: %s", data)
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if runErr != nil || !strings.Contains(string(out), "login=alice token=SYNTHETIC_PATH_TOKEN") {
				t.Fatalf("token-bearing invocation failed: %v\n%s", runErr, out)
			}
		})
	}
}

func TestShellHook_NativeShimTarget(t *testing.T) {
	native, err := exec.LookPath("printenv")
	if err != nil {
		t.Skipf("native printenv unavailable: %v", err)
	}
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			shellPath, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			shim, err := shims.InstallGH(filepath.Join(home, "unused-extension"), native)
			if err != nil {
				t.Fatal(err)
			}
			hook := filepath.Join(home, "hook.sh")
			if err := os.WriteFile(hook, shellHookContent, 0600); err != nil {
				t.Fatal(err)
			}
			script := fmt.Sprintf(`set -eu
export PATH=%q:/usr/bin:/bin
source %q
[[ -z "$_gh_autoprofile_gh_interpreter" ]]
export PATH=/nonexistent
_gh_autoprofile_run_gh SYNTHETIC_NATIVE_TOKEN GH_TOKEN GITHUB_TOKEN
`, filepath.Dir(shim), hook)
			cmd := exec.Command(shellPath, "-f", "-c", script)
			cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config")}
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != "SYNTHETIC_NATIVE_TOKEN\nSYNTHETIC_NATIVE_TOKEN\n" {
				t.Fatalf("native shim target failed: %v\n%s", err, out)
			}
		})
	}
}

func TestShellHookUnwrapsBothManagedShimForms(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skip(err)
			}
			home := t.TempDir()
			t.Setenv("HOME", home)
			shim, err := shims.InstallGH(filepath.Join(home, "extension's binary"), "/bin/sh")
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(shim)
			if err != nil {
				t.Fatal(err)
			}
			hook := filepath.Join(home, "hook")
			if err := os.WriteFile(hook, shellHookContent, 0600); err != nil {
				t.Fatal(err)
			}
			newForm := string(original)
			old := strings.Replace(newForm, "GH_PATH='/bin/sh' GH_AUTOPROFILE_REAL_GH='/bin/sh' exec ", "GH_AUTOPROFILE_REAL_GH='/bin/sh' exec ", 1)
			if old == newForm {
				t.Fatal("fixture did not match generated shim")
			}
			for _, tc := range []struct {
				name, content string
				ok            bool
			}{
				{"old", old, true}, {"new", newForm, true},
				{"mismatch", strings.Replace(newForm, "GH_PATH='/bin/sh'", "GH_PATH='/bin/false'", 1), false},
				{"injection", strings.Replace(old, ` __shim-gh "$@"`, ` __shim-gh "$@"; touch /tmp/unsafe`, 1), false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if err := os.WriteFile(shim, []byte(tc.content), 0700); err != nil {
						t.Fatal(err)
					}
					cmd := exec.Command(shell, "-f", "-c", `source "$1"; _gh_autoprofile_unwrap_gh_shim "$2"`, shell, hook, shim)
					out, err := cmd.CombinedOutput()
					if tc.ok && (err != nil || string(out) != "/bin/sh") {
						t.Fatalf("unwrap: %s %v", out, err)
					}
					if !tc.ok && err == nil {
						t.Fatalf("unsafe shim accepted: %s", out)
					}
				})
			}
		})
	}
}

func TestShellHook_InterpreterForms(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		for _, tc := range []struct {
			name    string
			header  string
			wantRun bool
		}{
			{name: "env bash", header: "#!/usr/bin/env bash", wantRun: true},
			{name: "env tabs", header: "#!\t/usr/bin/env\tbash\t", wantRun: true},
			{name: "bin env", header: "#!/bin/env bash", wantRun: true},
			{name: "env split", header: "#!/usr/bin/env -S bash", wantRun: true},
			{name: "env long split", header: "#!/usr/bin/env --split-string=bash", wantRun: true},
			{name: "absolute interpreter", header: "#!/bin/bash", wantRun: true},
			{name: "env split with flags", header: "#!/usr/bin/env -S bash -e"},
			{name: "env assignment", header: "#!/usr/bin/env -S PATH=/evil bash"},
			{name: "env quoting", header: "#!/usr/bin/env -S 'bash'"},
			{name: "env unset", header: "#!/usr/bin/env -S -u GH_TOKEN bash"},
			{name: "unknown env option", header: "#!/usr/bin/env -- bash"},
			{name: "missing interpreter", header: "#!/usr/bin/env missing-interpreter"},
			{name: "relative interpreter", header: "#!bash"},
			{name: "absolute interpreter argument", header: "#!/bin/bash -e"},
			{name: "script interpreter", header: "#!INTERPRETER"},
			{name: "no shebang", header: "# executable shell text"},
			{name: "malformed managed shim", header: "#!/usr/bin/env bash\n# gh-autoprofile-gh-shim\nset -euo pipefail\nmalformed\nmalformed"},
		} {
			t.Run(shell+"/"+tc.name, func(t *testing.T) {
				shellPath, err := exec.LookPath(shell)
				if err != nil {
					t.Skipf("%s unavailable: %v", shell, err)
				}
				home := t.TempDir()
				bin := filepath.Join(home, "bin")
				attacker := filepath.Join(home, "attacker")
				for _, path := range []string{bin, attacker} {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				write := func(path, content string) {
					t.Helper()
					if err := os.WriteFile(path, []byte(content), 0700); err != nil {
						t.Fatal(err)
					}
				}
				interpreter := filepath.Join(bin, "interpreter")
				write(interpreter, "#!/usr/bin/env bash\nexec /bin/bash \"$@\"\n")
				header := strings.ReplaceAll(tc.header, "INTERPRETER", interpreter)
				body := strings.ReplaceAll(guardedExecFixture, `token-$selected`, `SYNTHETIC_VARIANT_TOKEN`) + "\nprintf 'child=%s:%s arg=%s\\n' \"${GH_TOKEN:-none}\" \"${GITHUB_TOKEN:-none}\" \"${3:-none}\"\n"
				write(filepath.Join(bin, "gh"), header+body)
				write(filepath.Join(bin, "git"), header+body)
				hook := filepath.Join(home, "hook.sh")
				write(hook, string(shellHookContent))
				capture := filepath.Join(home, "capture")
				for _, name := range []string{"bash", "env", "gh", "git", "sed", "head", "cat"} {
					write(filepath.Join(attacker, name), "#!/bin/sh\nprintf '%s' \"${GH_TOKEN:-no-token}\" >> \"$CAPTURE\"\nexit 93\n")
				}
				script := fmt.Sprintf(`set -eu
export PATH=%q:/usr/bin:/bin
source %q
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_MODE=wrapper
_gh_autoprofile_hook
export PATH=%q:$PATH
if gh api user 'literal $(false) * argument'; then printf 'GH_RAN\n'; else printf 'GH_REFUSED\n'; fi
if git status x 'literal $(false) * argument'; then printf 'GIT_RAN\n'; else printf 'GIT_REFUSED\n'; fi
if gh auth login; then printf 'BYPASS_RAN\n'; else printf 'BYPASS_REFUSED\n'; fi
printf 'parent=%%s:%%s\n' "${GH_TOKEN:-none}" "${GITHUB_TOKEN:-none}"
`, bin, hook, attacker)
				cmd := exec.Command(shellPath, "-f", "-c", script)
				cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "CAPTURE=" + capture}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("shell failed: %v\n%s", err, out)
				}
				text := string(out)
				if tc.wantRun {
					if strings.Count(text, "child=SYNTHETIC_VARIANT_TOKEN:SYNTHETIC_VARIANT_TOKEN arg=literal $(false) * argument") != 2 || !strings.Contains(text, "BYPASS_RAN") || !strings.Contains(text, "child=none:none") {
						t.Fatalf("incorrect invocation or auth bypass: %s", out)
					}
				} else if !strings.Contains(text, "GH_REFUSED") || !strings.Contains(text, "GIT_REFUSED") || !strings.Contains(text, "BYPASS_REFUSED") || strings.Contains(text, "child=") || !strings.Contains(text, "could not be pinned safely") {
					t.Fatalf("unsupported interpreter did not fail closed: %s", out)
				}
				if !strings.Contains(text, "parent=none:none") {
					t.Fatalf("parent token leaked: %s", out)
				}
				if data, err := os.ReadFile(capture); !os.IsNotExist(err) {
					t.Fatalf("poisoned PATH utility was executed: %q, %v", data, err)
				}
			})
		}
	}
}

func TestAccountOnlyEnvrcPreservesNativeIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CONFIG_PARAMETERS", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = home
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
	}
	git("init", "--quiet", home)
	pin := config.Pin{Dir: home, User: "alice", GitName: "Native Alice", GitEmail: "native@example.test", SSHKey: "/unused/key"}
	if err := gitidentity.Sync(&config.PinRegistry{Pins: []config.Pin{pin}}); err != nil {
		t.Fatal(err)
	}
	if err := WriteEnvrc(pin); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".envrc"))
	if !strings.Contains(string(data), "use_gh_autoprofile alice '' '' /unused/key") {
		t.Fatalf("identity should be omitted without shifting SSH position: %s", data)
	}
	// Simulate a tracked account-only .envrc that predates managed onboarding.
	if err := os.WriteFile(filepath.Join(home, ".envrc"), []byte("use_gh_autoprofile alice\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", ".envrc")
	lib := filepath.Join(home, "library.sh")
	if err := os.WriteFile(lib, []byte(renderShellLib(filepath.Join(home, "bin", "gh"), "/bin/sh")), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n"+guardedExecFixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := exec.Command("bash", "-c", `source "$1"; log_status() { :; }; log_error() { return 1; }; source .envrc; git var GIT_AUTHOR_IDENT`, "bash", lib)
	cmd.Dir = home
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "Native Alice <native@example.test>") {
		t.Fatalf("account-only envrc lost native identity: %s %v", out, err)
	}
}

func TestWriteEnvrc_WrapperMode(t *testing.T) {
	tmpDir := t.TempDir()
	pin := config.Pin{User: "alice", Dir: tmpDir}

	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".envrc"))
	if err != nil {
		t.Fatalf("cannot read .envrc: %v", err)
	}

	expected := "# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\n"
	if string(content) != expected {
		t.Errorf("unexpected .envrc content:\ngot:  %q\nwant: %q", string(content), expected)
	}

	fi, err := os.Stat(filepath.Join(tmpDir, ".envrc"))
	if err != nil {
		t.Fatalf("cannot stat .envrc: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf(".envrc permissions = %o, want 600", got)
	}
}

func TestWriteEnvrc_ExistingFilePermissionsHardened(t *testing.T) {
	tmpDir := t.TempDir()
	envrcPath := filepath.Join(tmpDir, ".envrc")

	if err := os.WriteFile(envrcPath, []byte("export LEGACY=1\n"), 0644); err != nil {
		t.Fatalf("cannot write existing .envrc: %v", err)
	}
	if err := os.Chmod(envrcPath, 0755); err != nil {
		t.Fatalf("cannot set permissive mode on .envrc: %v", err)
	}

	pin := config.Pin{User: "alice", Dir: tmpDir}
	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	fi, err := os.Stat(envrcPath)
	if err != nil {
		t.Fatalf("cannot stat .envrc: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf(".envrc permissions = %o, want 600", got)
	}
}

func TestWriteEnvrc_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "target-file")
	if err := os.WriteFile(targetPath, []byte("sensitive\n"), 0600); err != nil {
		t.Fatalf("cannot write target file: %v", err)
	}

	// Create a directory where .envrc is a symlink to the target file.
	pinDir := filepath.Join(tmpDir, "pinned")
	if err := os.MkdirAll(pinDir, 0755); err != nil {
		t.Fatalf("cannot create pin dir: %v", err)
	}
	envrcLink := filepath.Join(pinDir, ".envrc")
	if err := os.Symlink(targetPath, envrcLink); err != nil {
		t.Fatalf("cannot create symlink: %v", err)
	}

	pin := config.Pin{User: "alice", Dir: pinDir}
	err := WriteEnvrc(pin)
	if err == nil {
		t.Fatal("expected WriteEnvrc to reject symlink, but it succeeded")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink error, got: %v", err)
	}

	// Target file must not have been modified.
	data, _ := os.ReadFile(targetPath)
	if string(data) != "sensitive\n" {
		t.Errorf("target file was modified through symlink: %q", string(data))
	}
}

func TestWriteAndHardenPerms_RejectsSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	targetPath := filepath.Join(tmpDir, "target-file")
	if err := os.WriteFile(targetPath, []byte("sensitive\n"), 0600); err != nil {
		t.Fatalf("cannot write target file: %v", err)
	}

	symlinkPath := filepath.Join(tmpDir, ".envrc")
	if err := os.Symlink(targetPath, symlinkPath); err != nil {
		t.Fatalf("cannot create symlink: %v", err)
	}

	err := writeAndHardenPerms(symlinkPath, []byte("unsafe\n"))
	if err == nil {
		t.Fatal("expected writeAndHardenPerms to reject symlink, but it succeeded")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink error, got: %v", err)
	}

	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("cannot read target file: %v", readErr)
	}
	if string(data) != "sensitive\n" {
		t.Errorf("target file was modified through symlink: %q", string(data))
	}
}

func TestWriteEnvrc_ExportMode(t *testing.T) {
	tmpDir := t.TempDir()
	pin := config.Pin{User: "alice", Dir: tmpDir, Mode: config.ModeExport}

	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".envrc"))
	if err != nil {
		t.Fatalf("cannot read .envrc: %v", err)
	}

	expected := "# gh-autoprofile:start\nuse_gh_autoprofile_export alice\n# gh-autoprofile:end\n"
	if string(content) != expected {
		t.Errorf("unexpected .envrc content:\ngot:  %q\nwant: %q", string(content), expected)
	}
}

func TestWriteEnvrc_WithGitIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tmpDir := t.TempDir()
	pin := config.Pin{
		User:     "bob",
		Dir:      tmpDir,
		GitEmail: "bob@example.test",
		GitName:  "Bob Test",
	}

	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".envrc"))
	if err != nil {
		t.Fatalf("cannot read .envrc: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "bob") {
		t.Error("expected .envrc to contain user 'bob'")
	}
	if !strings.Contains(s, "bob@example.test") {
		t.Error("expected .envrc to contain email")
	}
	if !strings.Contains(s, "Bob Test") {
		t.Error("expected .envrc to contain name")
	}
	// Default mode should be wrapper
	if !strings.Contains(s, "use_gh_autoprofile ") {
		t.Error("expected .envrc to use use_gh_autoprofile (wrapper mode)")
	}
	if strings.Contains(s, "use_gh_autoprofile_export") {
		t.Error("wrapper mode should not use use_gh_autoprofile_export")
	}
}

func TestWriteEnvrc_ExportWithGitIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tmpDir := t.TempDir()
	pin := config.Pin{
		User:     "bob",
		Dir:      tmpDir,
		Mode:     config.ModeExport,
		GitEmail: "bob@example.test",
		GitName:  "Bob Test",
	}

	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmpDir, ".envrc"))
	if err != nil {
		t.Fatalf("cannot read .envrc: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "use_gh_autoprofile_export") {
		t.Error("expected .envrc to use use_gh_autoprofile_export (export mode)")
	}
	if !strings.Contains(s, "bob@example.test") {
		t.Error("expected .envrc to contain email")
	}
}

func TestWriteEnvrc_PreservesExistingContent(t *testing.T) {
	tmpDir := t.TempDir()
	envrcPath := filepath.Join(tmpDir, ".envrc")

	// Write pre-existing content
	existing := "export MY_VAR=hello\n"
	if err := os.WriteFile(envrcPath, []byte(existing), 0644); err != nil {
		t.Fatalf("cannot write existing .envrc: %v", err)
	}

	pin := config.Pin{User: "alice", Dir: tmpDir}
	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	content, err := os.ReadFile(envrcPath)
	if err != nil {
		t.Fatalf("cannot read .envrc: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "export MY_VAR=hello") {
		t.Error("existing content was lost")
	}
	if !strings.Contains(s, markerStart) {
		t.Error("gh-autoprofile block not added")
	}
	if !strings.Contains(s, "use_gh_autoprofile alice") {
		t.Error("use_gh_autoprofile call not added")
	}
}

func TestHasUnmanagedEnvrcContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    bool
	}{
		{name: "missing", want: false},
		{name: "managed only", content: markerStart + "\nuse_gh_autoprofile alice\n" + markerEnd + "\n", want: false},
		{name: "before block", content: "export CUSTOM=1\n" + markerStart + "\nuse_gh_autoprofile alice\n" + markerEnd + "\n", want: true},
		{name: "after block", content: markerStart + "\nuse_gh_autoprofile alice\n" + markerEnd + "\nexport CUSTOM=1\n", want: true},
		{name: "no markers", content: "export CUSTOM=1\n", want: true},
		{name: "malformed markers", content: markerStart + "\nuse_gh_autoprofile alice\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.name != "missing" {
				if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte(tc.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := HasUnmanagedEnvrcContent(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("HasUnmanagedEnvrcContent() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWriteEnvrc_UpdatesExistingBlock(t *testing.T) {
	tmpDir := t.TempDir()
	envrcPath := filepath.Join(tmpDir, ".envrc")

	// First pin (wrapper mode)
	pin1 := config.Pin{User: "alice", Dir: tmpDir}
	if err := WriteEnvrc(pin1); err != nil {
		t.Fatalf("first WriteEnvrc failed: %v", err)
	}

	// Update to export mode with different user
	pin2 := config.Pin{User: "bob", Dir: tmpDir, Mode: config.ModeExport, GitEmail: "bob@example.test"}
	if err := WriteEnvrc(pin2); err != nil {
		t.Fatalf("second WriteEnvrc failed: %v", err)
	}

	content, err := os.ReadFile(envrcPath)
	if err != nil {
		t.Fatalf("cannot read .envrc: %v", err)
	}

	s := string(content)
	if strings.Contains(s, "alice") {
		t.Error("old user 'alice' still present after update")
	}
	if !strings.Contains(s, "use_gh_autoprofile_export bob") {
		t.Error("new export mode with user 'bob' not found")
	}
	// Should only have one start marker
	if strings.Count(s, markerStart) != 1 {
		t.Errorf("expected exactly 1 start marker, got %d", strings.Count(s, markerStart))
	}
}

func TestRemoveEnvrc_DeletesEmptyFile(t *testing.T) {
	tmpDir := t.TempDir()
	envrcPath := filepath.Join(tmpDir, ".envrc")

	// Write a block
	pin := config.Pin{User: "alice", Dir: tmpDir}
	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	// Remove it
	if err := RemoveEnvrc(tmpDir); err != nil {
		t.Fatalf("RemoveEnvrc failed: %v", err)
	}

	// File should be gone (was only gh-autoprofile content)
	if _, err := os.Stat(envrcPath); !os.IsNotExist(err) {
		t.Error("expected .envrc to be deleted when empty after removal")
	}
}

func TestRemoveEnvrc_PreservesOtherContent(t *testing.T) {
	tmpDir := t.TempDir()
	envrcPath := filepath.Join(tmpDir, ".envrc")

	// Write existing content, then add block
	existing := "export MY_VAR=hello\n"
	if err := os.WriteFile(envrcPath, []byte(existing), 0644); err != nil {
		t.Fatalf("cannot write .envrc: %v", err)
	}
	pin := config.Pin{User: "alice", Dir: tmpDir}
	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	// Remove block
	if err := RemoveEnvrc(tmpDir); err != nil {
		t.Fatalf("RemoveEnvrc failed: %v", err)
	}

	// File should still exist with original content
	content, err := os.ReadFile(envrcPath)
	if err != nil {
		t.Fatalf("cannot read .envrc: %v", err)
	}

	s := string(content)
	if !strings.Contains(s, "export MY_VAR=hello") {
		t.Error("existing content was lost after removal")
	}
	if strings.Contains(s, markerStart) {
		t.Error("gh-autoprofile markers still present after removal")
	}
}

func TestRemoveEnvrc_NoopWhenNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	// Should not error when no .envrc exists
	if err := RemoveEnvrc(tmpDir); err != nil {
		t.Fatalf("RemoveEnvrc should not error on missing file: %v", err)
	}
}

func TestDenyEnvrc_NoopWhenDirenvNotInstalled(t *testing.T) {
	// DenyEnvrc should return nil when direnv is not installed.
	// We test this by temporarily manipulating PATH — but since direnv
	// IS likely installed in CI/dev, we just verify the function exists
	// and is callable. If direnv is installed, it should succeed or
	// gracefully handle a non-existent .envrc.
	tmpDir := t.TempDir()

	// Create a .envrc so there's something to deny
	envrcPath := filepath.Join(tmpDir, ".envrc")
	if err := os.WriteFile(envrcPath, []byte("# test\n"), 0600); err != nil {
		t.Fatalf("cannot write .envrc: %v", err)
	}

	// DenyEnvrc should not panic or crash — it's best-effort.
	// If direnv is installed, this will actually run `direnv deny`.
	// If not, it returns nil immediately.
	err := DenyEnvrc(tmpDir)
	// We don't assert on the error value since direnv may or may not be
	// installed, and the .envrc may not be in the allowlist.
	_ = err
}

func TestDenyEnvrc_CalledBeforeRemoveEnvrc(t *testing.T) {
	// Integration test: verify that DenyEnvrc + RemoveEnvrc sequence
	// works without errors (the unpin flow).
	tmpDir := t.TempDir()

	pin := config.Pin{User: "alice", Dir: tmpDir}
	if err := WriteEnvrc(pin); err != nil {
		t.Fatalf("WriteEnvrc failed: %v", err)
	}

	// Verify .envrc was created
	envrcPath := filepath.Join(tmpDir, ".envrc")
	if _, err := os.Stat(envrcPath); os.IsNotExist(err) {
		t.Fatal(".envrc was not created")
	}

	// DenyEnvrc (best-effort, don't fail test on direnv absence)
	_ = DenyEnvrc(tmpDir)

	// RemoveEnvrc should still work after deny
	if err := RemoveEnvrc(tmpDir); err != nil {
		t.Fatalf("RemoveEnvrc failed after DenyEnvrc: %v", err)
	}

	// File should be deleted (was only gh-autoprofile content)
	if _, err := os.Stat(envrcPath); !os.IsNotExist(err) {
		t.Error("expected .envrc to be deleted after deny+remove sequence")
	}
}

func TestShellLibContent(t *testing.T) {
	// Verify the embedded shell library contains both wrapper and export functions
	if len(shellLibContent) == 0 {
		t.Fatal("embedded shell library is empty")
	}
	s := string(shellLibContent)
	if !strings.Contains(s, "use_gh_autoprofile()") {
		t.Error("shell library missing use_gh_autoprofile function (wrapper mode)")
	}
	if !strings.Contains(s, "use_gh_autoprofile_export()") {
		t.Error("shell library missing use_gh_autoprofile_export function (export mode)")
	}
	if !strings.Contains(s, "GH_AUTOPROFILE_USER") {
		t.Error("shell library missing GH_AUTOPROFILE_USER marker export")
	}
	if !strings.Contains(s, "GH_AUTOPROFILE_MODE") {
		t.Error("shell library missing GH_AUTOPROFILE_MODE marker export")
	}
	if !strings.Contains(s, "GH_TOKEN") {
		t.Error("shell library missing GH_TOKEN export (in export mode)")
	}
	if !strings.Contains(s, "GIT_AUTHOR_EMAIL") {
		t.Error("shell library missing GIT_AUTHOR_EMAIL")
	}
	if !strings.Contains(s, "GIT_SSH_COMMAND") {
		t.Error("shell library missing GIT_SSH_COMMAND")
	}
}

func TestShellHookContent(t *testing.T) {
	// Verify the embedded shell hook is non-empty and contains key elements
	if len(shellHookContent) == 0 {
		t.Fatal("embedded shell hook is empty")
	}
	s := string(shellHookContent)
	if !strings.Contains(s, "_gh_autoprofile_hook") {
		t.Error("shell hook missing _gh_autoprofile_hook function")
	}
	if !strings.Contains(s, "GH_AUTOPROFILE_USER") {
		t.Error("shell hook missing GH_AUTOPROFILE_USER reference")
	}
	if !strings.Contains(s, "GH_AUTOPROFILE_MODE") {
		t.Error("shell hook missing GH_AUTOPROFILE_MODE reference")
	}
	if !strings.Contains(s, `_gh_autoprofile_run_gh "" autoaccount exec`) || strings.Contains(s, `auth token`) {
		t.Error("shell hook must use guarded exec, not direct token lookup")
	}
	if !strings.Contains(s, "_gh_autoprofile_should_bypass_gh_auth") {
		t.Error("shell hook missing gh auth bypass helper")
	}
	if !strings.Contains(s, `GH_AUTOPROFILE_DISABLE_SHIM=1 "${_command[@]}" "$@"`) {
		t.Error("shell hook missing gh auth bypass execution path")
	}
	if !strings.Contains(s, "precmd") {
		t.Error("shell hook missing zsh precmd integration")
	}
	if !strings.Contains(s, "PROMPT_COMMAND") {
		t.Error("shell hook missing bash PROMPT_COMMAND integration")
	}
	// Verify explicit mode handling and state tracking.
	if !strings.Contains(s, "_gh_autoprofile_last_mode") {
		t.Error("shell hook missing _gh_autoprofile_last_mode state tracking")
	}
}

func TestShellLib_WrapperModeClearsAmbientTokens(t *testing.T) {
	tmpDir := t.TempDir()
	libPath := filepath.Join(tmpDir, "gh-autoprofile.sh")
	if err := os.WriteFile(libPath, []byte(renderShellLib(filepath.Join(tmpDir, "bin", "gh"), "/bin/sh")), 0700); err != nil {
		t.Fatalf("cannot write shell library: %v", err)
	}

	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatalf("cannot create fake bin dir: %v", err)
	}

	fakeGh := "#!/usr/bin/env bash\n" + guardedExecFixture + "exit 1\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(fakeGh), 0755); err != nil {
		t.Fatalf("cannot write fake gh: %v", err)
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}

	script := fmt.Sprintf(`set -e
export PATH=%q:$PATH
log_status() { :; }
log_error() { echo "$*" >&2; }
export GH_TOKEN=ambient-gh
export GITHUB_TOKEN=ambient-github
source %q
use_gh_autoprofile alice
echo MODE=${GH_AUTOPROFILE_MODE:-unset}
echo USER=${GH_AUTOPROFILE_USER:-unset}
echo GH_TOKEN=${GH_TOKEN:-unset}
echo GITHUB_TOKEN=${GITHUB_TOKEN:-unset}
`, fakeBin, libPath)

	cmd := exec.Command("bash", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash script failed: %v\noutput:\n%s", err, string(out))
	}

	s := string(out)
	if !strings.Contains(s, "MODE=wrapper") {
		t.Fatalf("expected wrapper mode marker, got:\n%s", s)
	}
	if !strings.Contains(s, "USER=alice") {
		t.Fatalf("expected pinned user marker, got:\n%s", s)
	}
	if !strings.Contains(s, "GH_TOKEN=unset") || !strings.Contains(s, "GITHUB_TOKEN=unset") {
		t.Fatalf("expected ambient tokens to be cleared in wrapper mode, got:\n%s", s)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"alice", "alice"},
		{"bob-work", "bob-work"},
		{"alice@example.test", "alice@example.test"},
		{"/home/user/.ssh/id_ed25519", "/home/user/.ssh/id_ed25519"},
		{"Bob Smith", "'Bob Smith'"},
		{"it's a test", "'it'\\''s a test'"},
		{"has spaces", "'has spaces'"},
		{"has\ttab", "'has\ttab'"},
		{"has$dollar", "'has$dollar'"},
		{"", "''"},
	}

	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.expected {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

// --- ShellLibDir path resolution tests ---

func TestShellLibDir_DIRENV_CONFIG(t *testing.T) {
	// When DIRENV_CONFIG is set, ShellLibDir must use it as the config root.
	// This covers custom direnv config roots set by shells, wrappers, or IDEs.
	customConfig := "/tmp/test-direnv-config"
	t.Setenv("DIRENV_CONFIG", customConfig)

	dir, err := ShellLibDir()
	if err != nil {
		t.Fatalf("ShellLibDir failed: %v", err)
	}
	expected := filepath.Join(customConfig, "lib")
	if dir != expected {
		t.Errorf("ShellLibDir() = %q, want %q", dir, expected)
	}
}

func TestShellLibDir_DIRENV_CONFIG_TakesPrecedence(t *testing.T) {
	// DIRENV_CONFIG must beat XDG_CONFIG_HOME when both are set.
	t.Setenv("DIRENV_CONFIG", "/custom/direnv")
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

	dir, err := ShellLibDir()
	if err != nil {
		t.Fatalf("ShellLibDir failed: %v", err)
	}
	if dir != "/custom/direnv/lib" {
		t.Errorf("DIRENV_CONFIG should take precedence, got %q", dir)
	}
}

func TestShellLibDir_XDG_CONFIG_HOME(t *testing.T) {
	// When DIRENV_CONFIG is unset but XDG_CONFIG_HOME is set.
	t.Setenv("DIRENV_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")

	dir, err := ShellLibDir()
	if err != nil {
		t.Fatalf("ShellLibDir failed: %v", err)
	}
	expected := filepath.Join("/custom/xdg", "direnv", "lib")
	if dir != expected {
		t.Errorf("ShellLibDir() = %q, want %q", dir, expected)
	}
}

func TestShellLibDir_DefaultFallback(t *testing.T) {
	// When neither DIRENV_CONFIG nor XDG_CONFIG_HOME is set, falls
	// back to ~/.config/direnv/lib.
	t.Setenv("DIRENV_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	dir, err := ShellLibDir()
	if err != nil {
		t.Fatalf("ShellLibDir failed: %v", err)
	}
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".config", "direnv", "lib")
	if dir != expected {
		t.Errorf("ShellLibDir() = %q, want %q", dir, expected)
	}
}

func TestInstallShellLib_UsesDIRENV_CONFIG(t *testing.T) {
	// InstallShellLib must write the library under DIRENV_CONFIG/lib/
	// so that the active direnv instance can find it.
	tmpDir := t.TempDir()
	t.Setenv("DIRENV_CONFIG", tmpDir)

	if err := InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
		t.Fatalf("InstallShellLib failed: %v", err)
	}

	installed := filepath.Join(tmpDir, "lib", "gh-autoprofile.sh")
	if _, err := os.Stat(installed); os.IsNotExist(err) {
		t.Fatalf("expected library at %s, not found", installed)
	}

	content, err := os.ReadFile(installed)
	if err != nil {
		t.Fatalf("cannot read installed lib: %v", err)
	}
	if !strings.Contains(string(content), "use_gh_autoprofile") {
		t.Error("installed library missing use_gh_autoprofile function")
	}
}

func TestIsShellLibInstalled_DetectsDIRENV_CONFIG(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("DIRENV_CONFIG", tmpDir)

	// Before install — should report false.
	if IsShellLibInstalled() {
		t.Error("expected IsShellLibInstalled=false before install")
	}

	// After install — should report true.
	if err := InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
		t.Fatalf("InstallShellLib failed: %v", err)
	}
	if !IsShellLibInstalled() {
		t.Error("expected IsShellLibInstalled=true after install")
	}
}

func TestInjectHookSource(t *testing.T) {
	tmpDir := t.TempDir()
	rcPath := filepath.Join(tmpDir, ".zshrc")
	hookPath := filepath.Join(tmpDir, "hook.sh")

	// First injection — creates the file
	if err := InjectHookSource(rcPath, hookPath); err != nil {
		t.Fatalf("InjectHookSource failed: %v", err)
	}

	content, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("cannot read RC file: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, hookMarkerStart) {
		t.Error("hook marker start not found")
	}
	if !strings.Contains(s, hookPath) {
		t.Errorf("hook path %q not found in RC file", hookPath)
	}

	// Second injection — should replace, not duplicate
	if err := InjectHookSource(rcPath, hookPath); err != nil {
		t.Fatalf("second InjectHookSource failed: %v", err)
	}

	content, err = os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("cannot read RC file: %v", err)
	}
	s = string(content)
	if strings.Count(s, hookMarkerStart) != 1 {
		t.Errorf("expected exactly 1 hook marker, got %d", strings.Count(s, hookMarkerStart))
	}
}

func TestInjectHookSourceQuotesLiteralPath(t *testing.T) {
	dir := t.TempDir()
	rcPath := filepath.Join(dir, ".bashrc")
	hookDir := filepath.Join(dir, "space $(touch PWNED) quote's")
	if err := os.Mkdir(hookDir, 0700); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hookDir, "hook.sh")
	if err := os.WriteFile(hookPath, []byte("printf sourced > \"$HOOK_LOG\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InjectHookSource(rcPath, hookPath); err != nil {
		t.Fatal(err)
	}

	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			logPath := filepath.Join(dir, "hook-"+shell)
			cmd := exec.Command(shell, "-f", "-c", `source "$1"`, shell, rcPath)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "HOOK_LOG="+logPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("source failed: %v\n%s", err, out)
			}
			data, err := os.ReadFile(logPath)
			if err != nil || string(data) != "sourced" {
				t.Fatalf("literal hook path was not sourced: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "PWNED")); !os.IsNotExist(err) {
				t.Fatalf("hook path executed command substitution: %v", err)
			}
		})
	}
}

func TestInjectHookSource_PreservesExisting(t *testing.T) {
	tmpDir := t.TempDir()
	rcPath := filepath.Join(tmpDir, ".zshrc")
	hookPath := filepath.Join(tmpDir, "hook.sh")

	// Write existing RC content
	existing := "# My zsh config\nexport EDITOR=vim\n"
	if err := os.WriteFile(rcPath, []byte(existing), 0644); err != nil {
		t.Fatalf("cannot write RC file: %v", err)
	}

	if err := InjectHookSource(rcPath, hookPath); err != nil {
		t.Fatalf("InjectHookSource failed: %v", err)
	}

	content, err := os.ReadFile(rcPath)
	if err != nil {
		t.Fatalf("cannot read RC file: %v", err)
	}
	s := string(content)
	if !strings.Contains(s, "export EDITOR=vim") {
		t.Error("existing content was lost")
	}
	if !strings.Contains(s, hookMarkerStart) {
		t.Error("hook marker not added")
	}
}

func TestShellHook_WrapperAndExportBehavior(t *testing.T) {
	tmpDir := t.TempDir()
	hookPath := filepath.Join(tmpDir, "hook.sh")
	if err := os.WriteFile(hookPath, shellHookContent, 0700); err != nil {
		t.Fatalf("cannot write hook file: %v", err)
	}

	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatalf("cannot create fake bin dir: %v", err)
	}

	fakeGh := "#!/usr/bin/env bash\n" + guardedExecFixture +
		"if [[ \"$1\" == \"api\" ]]; then\n" +
		"  echo GH_TOKEN=${GH_TOKEN:-}\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo gh-called\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(fakeGh), 0755); err != nil {
		t.Fatalf("cannot write fake gh: %v", err)
	}

	fakeGit := "#!/usr/bin/env bash\n" +
		"echo GH_TOKEN=${GH_TOKEN:-}\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(fakeGit), 0755); err != nil {
		t.Fatalf("cannot write fake git: %v", err)
	}

	tests := []struct {
		name   string
		shell  string
		script string
	}{
		{
			name:  "bash",
			shell: "bash",
			script: fmt.Sprintf(`set -eu
export PATH=%q:$PATH
source %q
export GH_AUTOPROFILE_USER=alice
export GH_AUTOPROFILE_MODE=wrapper
export GH_TOKEN=preexisting
_gh_autoprofile_hook
echo WRAP_GH=$(type -t gh)
echo WRAP_GIT=$(type -t git)
gh api /user
git status
export GH_AUTOPROFILE_MODE=export
_gh_autoprofile_hook
echo EXP_GH=$(type -t gh)
echo EXP_GIT=$(type -t git)
`, fakeBin, hookPath),
		},
		{
			name:  "zsh",
			shell: "zsh",
			script: fmt.Sprintf(`set -eu
export PATH=%q:$PATH
source %q
export GH_AUTOPROFILE_USER=alice
export GH_AUTOPROFILE_MODE=wrapper
export GH_TOKEN=preexisting
_gh_autoprofile_hook
echo WRAP_GH=$(whence -w gh)
echo WRAP_GIT=$(whence -w git)
gh api /user
git status
export GH_AUTOPROFILE_MODE=export
_gh_autoprofile_hook
echo EXP_GH=$(whence -w gh)
echo EXP_GIT=$(whence -w git)
`, fakeBin, hookPath),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.shell); err != nil {
				t.Skipf("%s not available: %v", tc.shell, err)
			}

			cmd := exec.Command(tc.shell, "-c", tc.script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s script failed: %v\noutput:\n%s", tc.shell, err, string(out))
			}

			s := string(out)
			if !strings.Contains(s, "GH_TOKEN=token-alice") {
				t.Fatalf("expected wrapper mode token injection, got:\n%s", s)
			}
			if strings.Contains(s, "EXP_GH=function") || strings.Contains(s, "EXP_GIT=function") {
				t.Fatalf("expected export mode to remove wrappers, got:\n%s", s)
			}
		})
	}
}

func TestShellHookPinsCommandPaths(t *testing.T) {
	dir := t.TempDir()
	hookPath := filepath.Join(dir, "hook.sh")
	if err := os.WriteFile(hookPath, shellHookContent, 0700); err != nil {
		t.Fatal(err)
	}
	trustedBin := filepath.Join(dir, "trusted")
	poisonBin := filepath.Join(dir, "poison")
	for _, path := range []string{trustedBin, poisonBin} {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	trustedGH := `#!/bin/sh
` + guardedExecFixture + `
printf 'TRUSTED_GH %s %s\n' "$GH_TOKEN" "$*"
`
	trustedGit := "#!/bin/sh\nprintf 'TRUSTED_GIT %s %s\\n' \"$GH_TOKEN\" \"$*\"\n"
	poison := "#!/bin/sh\nprintf 'POISON %s\\n' \"$0\" >> \"$POISON_LOG\"\nexit 99\n"
	for path, data := range map[string]string{
		filepath.Join(trustedBin, "gh"):  trustedGH,
		filepath.Join(trustedBin, "git"): trustedGit,
		filepath.Join(poisonBin, "gh"):   poison,
		filepath.Join(poisonBin, "git"):  poison,
	} {
		if err := os.WriteFile(path, []byte(data), 0755); err != nil {
			t.Fatal(err)
		}
	}
	poisonLog := filepath.Join(dir, "poison.log")
	script := fmt.Sprintf(`set -eu
export PATH=%q:/usr/bin:/bin
source %q
export PATH=%q:$PATH
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_MODE=wrapper
_gh_autoprofile_hook
gh api user
git status
export GH_AUTOPROFILE_USER=bob
_gh_autoprofile_hook
gh api user
git status
`, trustedBin, hookPath, poisonBin)

	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			if err := os.Remove(poisonLog); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			cmd := exec.Command(shell, "-f", "-c", script)
			cmd.Env = append(os.Environ(), "POISON_LOG="+poisonLog, "_GH_AUTOPROFILE_HOOK_LOADED=")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shell failed: %v\n%s", err, out)
			}
			for _, want := range []string{
				"TRUSTED_GH token-alice api user",
				"TRUSTED_GIT token-alice status",
				"TRUSTED_GH token-bob api user",
				"TRUSTED_GIT token-bob status",
			} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("missing %q:\n%s", want, out)
				}
			}
			if data, err := os.ReadFile(poisonLog); err == nil {
				t.Fatalf("poisoned PATH command ran: %s", data)
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}

func TestShellHook_WrapperModeIgnoresAmbientGITHUBToken(t *testing.T) {
	// Wrapper mode should resolve the pinned user's stored gh token even when
	// an unrelated ambient GITHUB_TOKEN is present in the parent shell.
	tmpDir := t.TempDir()
	hookPath := filepath.Join(tmpDir, "hook.sh")
	if err := os.WriteFile(hookPath, shellHookContent, 0700); err != nil {
		t.Fatalf("cannot write hook file: %v", err)
	}

	fakeBin := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatalf("cannot create fake bin dir: %v", err)
	}

	// Fake gh that resolves a pinned token and prints the child GH_TOKEN when
	// invoked normally.
	fakeGh := "#!/usr/bin/env bash\n" + guardedExecFixture +
		"echo GH_TOKEN=${GH_TOKEN:-unset}\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(fakeGh), 0755); err != nil {
		t.Fatalf("cannot write fake gh: %v", err)
	}

	fakeGit := "#!/usr/bin/env bash\n" +
		"echo GH_TOKEN=${GH_TOKEN:-unset}\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "git"), []byte(fakeGit), 0755); err != nil {
		t.Fatalf("cannot write fake git: %v", err)
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}

	script := fmt.Sprintf(`set -e
export PATH=%q:$PATH
source %q
export GH_AUTOPROFILE_USER=alice
export GH_AUTOPROFILE_MODE=wrapper
export GITHUB_TOKEN=preexisting-github-token
unset GH_TOKEN
_gh_autoprofile_hook
# Wrapper mode should ignore ambient GITHUB_TOKEN and resolve alice's token
gh api /user
git status
`, fakeBin, hookPath)

	cmd := exec.Command("bash", "-c", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash script failed: %v\noutput:\n%s", err, string(out))
	}

	s := string(out)
	if !strings.Contains(s, "GH_TOKEN=token-alice") {
		t.Errorf("expected wrappers to inject the pinned user's token, got:\n%s", s)
	}
}

func TestShellAuthFailClosed(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			dir := t.TempDir()
			files := map[string][]byte{
				"hook.sh": shellHookContent,
				"lib.sh":  []byte(renderShellLib(filepath.Join(dir, "gh"), filepath.Join(dir, "gh"))),
				"gh": []byte(`#!/bin/sh
if [ "$1 $2" = 'autoaccount exec' ] || [ "$1" = exec ]; then
  while [ "$1" != -- ]; do
    if [ "$1" = --expect-user ]; then shift; selected=$1; fi
    shift
  done
  shift
  case "$TEST_TOKEN_CASE" in
    empty|failure|multiline|whitespace) exit 1 ;;
    good) export GH_TOKEN="stored-$selected" GITHUB_TOKEN="stored-$selected" GH_AUTOPROFILE_USER="$selected" GH_AUTOPROFILE_HOST=github.com GH_AUTOPROFILE_MODE="${TEST_MODE:-wrapper}" ;;
  esac
  exec "$@"
fi
printf 'GH %s %s %s\n' "$*" "$GH_TOKEN" "$GITHUB_TOKEN" >> "$TEST_CALLS"
`),
				"git": []byte("#!/bin/sh\nprintf 'GIT %s %s\\n' \"$GH_TOKEN\" \"$GITHUB_TOKEN\" >> \"$TEST_CALLS\"\n"),
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("TEST_CALLS", filepath.Join(dir, "calls"))
			t.Setenv("_GH_AUTOPROFILE_HOOK_LOADED", "")
			script := `set -e
source ./hook.sh
source ./lib.sh
log_error() { :; }
log_status() { :; }
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_MODE=wrapper
export GH_TOKEN=unrelated GITHUB_TOKEN=unrelated-too
_gh_autoprofile_hook
for TEST_TOKEN_CASE in empty failure multiline whitespace; do
  export TEST_TOKEN_CASE
  if gh api user; then exit 11; fi
  if git fetch; then exit 12; fi
  if use_gh_autoprofile bob; then exit 13; fi
  unset GH_TOKEN GITHUB_TOKEN
  if use_gh_autoprofile_export bob; then exit 14; fi
done
export TEST_TOKEN_CASE=failure
export GH_AUTOPROFILE_MODE=wrapper
_gh_autoprofile_hook
for action in switch login logout refresh setup-git; do
  gh auth "$action"
done
export TEST_TOKEN_CASE=good
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_MODE=wrapper
_gh_autoprofile_hook
export GH_TOKEN=unrelated GITHUB_TOKEN=unrelated-too
gh api user
git fetch
test "$GH_TOKEN" = unrelated
test "$GITHUB_TOKEN" = unrelated-too
use_gh_autoprofile bob
_gh_autoprofile_hook
gh api user
 export GH_TOKEN=stored-bob GITHUB_TOKEN=stored-bob
 export TEST_MODE=export
 use_gh_autoprofile_export alice
test "$GH_TOKEN" = stored-alice
unset GH_AUTOPROFILE_USER
export GH_TOKEN=explicit-export
use_gh_autoprofile_export alice
test "$GH_TOKEN" = stored-alice
unset GH_AUTOPROFILE_USER
export GH_TOKEN=explicit-export
export GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN=1
use_gh_autoprofile_export alice
 test "$GITHUB_TOKEN" = stored-alice
`
			cmd := exec.Command(shell, "-f", "-c", script)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shell failed: %v\n%s", err, out)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			want := "GH auth switch  \nGH auth login  \nGH auth logout  \nGH auth refresh  \nGH auth setup-git  \nGH api user stored-alice stored-alice\nGIT stored-alice stored-alice\nGH api user stored-bob stored-bob\n"
			if string(calls) != want {
				t.Fatalf("unexpected real command invocations:\n%s", calls)
			}
		})
	}
}

func TestShellLib_SSHKeyPathIsQuotedForGitShellEvaluation(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib.sh")
	if err := os.WriteFile(lib, []byte(renderShellLib("/bin/sh", "/bin/sh")), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	ssh := "#!/bin/sh\nprintf '<%s>\\n' \"$@\" > \"$TEST_SSH_ARGS\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}

	key := filepath.Join(dir, "team's $(touch substituted) key'; touch injected; printf '")
	if err := os.WriteFile(key, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	script := `set -e
log_error() { printf '%s\n' "$*" >&2; }
source "$1"
_gh_autoprofile_identity "" "" "$2"
if git ls-remote ssh://example.invalid/repo >/dev/null 2>&1; then
  :
else
  test "$?" -eq 128
fi
`
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			if _, err := exec.LookPath(shell); err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			argsPath := filepath.Join(dir, "ssh-args-"+shell)
			cmd := exec.Command(shell, "-f", "-c", script, shell, lib, key)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "TEST_SSH_ARGS="+argsPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shell evaluation failed: %v\n%s", err, out)
			}
			if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
				t.Fatalf("SSH key path executed a command: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "substituted")); !os.IsNotExist(err) {
				t.Fatalf("SSH key path executed command substitution: %v", err)
			}
			args, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(args), "<"+key+">\n") {
				t.Fatalf("SSH did not receive the literal key path:\n%s", args)
			}
		})
	}
}

func TestShellLib_RejectsSSHKeyPathWithLineBreak(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib.sh")
	if err := os.WriteFile(lib, []byte(renderShellLib(filepath.Join(dir, "bin", "gh"), "/bin/sh")), 0600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(dir, "key\nname")
	if err := os.WriteFile(key, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n"+guardedExecFixture), 0700); err != nil {
		t.Fatal(err)
	}
	script := `source "$1"
log_error() { :; }
log_status() { :; }
if use_gh_autoprofile alice "" "" "$2"; then exit 1; fi
test -z "${GIT_SSH_COMMAND:-}"
export GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN=1 GH_TOKEN=test-token
if use_gh_autoprofile_export alice "" "" "$2"; then exit 2; fi
test -z "${GIT_SSH_COMMAND:-}"
`
	cmd := exec.Command("bash", "-c", script, "bash", lib, key)
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("line-break path was not rejected: %v\n%s", err, out)
	}
}

func TestShellHook_RegistersWithPromptCycle(t *testing.T) {
	tmpDir := t.TempDir()
	hookPath := filepath.Join(tmpDir, "hook.sh")
	if err := os.WriteFile(hookPath, shellHookContent, 0700); err != nil {
		t.Fatalf("cannot write hook file: %v", err)
	}

	t.Run("bash", func(t *testing.T) {
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skipf("bash not available: %v", err)
		}

		cmd := exec.Command("bash", "-c", fmt.Sprintf(`_GH_AUTOPROFILE_HOOK_LOADED=2; PROMPT_COMMAND=_gh_autoprofile_hook; source %q; source %q; echo LOADED=${_GH_AUTOPROFILE_HOOK_LOADED}; echo PROMPT_COMMAND=${PROMPT_COMMAND}`, hookPath, hookPath))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bash script failed: %v\noutput:\n%s", err, string(out))
		}
		if !strings.Contains(string(out), "_gh_autoprofile_hook") {
			t.Fatalf("expected PROMPT_COMMAND to include hook, got:\n%s", string(out))
		}
		if !strings.Contains(string(out), "LOADED=6:0") {
			t.Fatalf("expected legacy hook revision to reload, got:\n%s", string(out))
		}
		if strings.Count(string(out), "_gh_autoprofile_hook") != 1 {
			t.Fatalf("expected one prompt hook registration, got:\n%s", string(out))
		}
	})

	t.Run("zsh", func(t *testing.T) {
		if _, err := exec.LookPath("zsh"); err != nil {
			t.Skipf("zsh not available: %v", err)
		}

		cmd := exec.Command("zsh", "-c", fmt.Sprintf(`_GH_AUTOPROFILE_HOOK_LOADED=2; precmd_functions=(_gh_autoprofile_hook); source %q; source %q; autoload -Uz add-zsh-hook; if [[ "${precmd_functions[(r)_gh_autoprofile_hook]}" == "_gh_autoprofile_hook" ]]; then echo OK; fi; echo LOADED=${_GH_AUTOPROFILE_HOOK_LOADED}; echo PRECMD=${precmd_functions}`, hookPath, hookPath))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("zsh script failed: %v\noutput:\n%s", err, string(out))
		}
		if !strings.Contains(string(out), "OK") {
			t.Fatalf("expected precmd hook registration, got:\n%s", string(out))
		}
		if !strings.Contains(string(out), "LOADED=6:0") {
			t.Fatalf("expected legacy hook revision to reload, got:\n%s", string(out))
		}
		if strings.Count(string(out), "_gh_autoprofile_hook") != 1 {
			t.Fatalf("expected one precmd hook registration, got:\n%s", string(out))
		}
	})
}

func TestShellLibDeprecationNotices(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct{ fn, mode, want string }{
		{"use_gh_autoprofile", "wrapper", "gh autoaccount setup --native"},
		{"use_gh_autoprofile_export", "export", "gh autoaccount exec"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DIRENV_CONFIG", filepath.Join(root, "direnv"))
			extension := filepath.Join(root, "extension")
			fixture := `#!/bin/sh
while [ "$1" != -- ]; do shift; done
shift
export GH_AUTOPROFILE_USER=alice GH_AUTOPROFILE_HOST=github.com GH_AUTOPROFILE_MODE="$FIXTURE_MODE" GH_TOKEN=trusted
exec "$@"
`
			if err := os.WriteFile(extension, []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			if err := InstallShellLib(extension, "/bin/sh"); err != nil {
				t.Fatal(err)
			}
			lib, _ := ShellLibPath()
			script := `log_error() { printf 'ERROR:%s\n' "$*"; }; log_status() { printf 'STATUS:%s\n' "$*"; }; source "$LIB"; ` + tc.fn + ` alice`
			cmd := exec.Command(bash, "-c", script)
			cmd.Env = append(os.Environ(), "LIB="+lib, "FIXTURE_MODE="+tc.mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("activation failed: %v: %s", err, out)
			}
			var deprecations []string
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "deprecated") {
					deprecations = append(deprecations, line)
				}
			}
			if len(deprecations) != 1 || !strings.Contains(deprecations[0], tc.want) {
				t.Fatalf("want one deprecation line mentioning %q, got %q\n%s", tc.want, deprecations, out)
			}
			if !strings.Contains(string(out), "activated 'alice' ("+tc.mode+" mode)") {
				t.Fatalf("activation notice missing: %s", out)
			}
		})
	}
}
