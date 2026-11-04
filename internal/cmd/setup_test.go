package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

func onboardingHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("DIRENV_REQUIRED", "")
	t.Setenv("DIRENV_CONFIG", filepath.Join(home, "direnv"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("SHELL", "/bin/bash")
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_AUTOPROFILE_USER", "GH_AUTOPROFILE_MODE", "GH_AUTOPROFILE_REAL_GH", "GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_NAME"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{
		"gh":     "#!/bin/sh\ncase \"$*\" in\n  --version) printf 'gh version 2.50.0\\n';;\n  'auth status') printf 'github.com\\n  Logged in to github.com account alice (keyring)\\n';;\n  'auth token --user alice --hostname github.com') printf 'test-token\\n';;\n  *) exit 1;;\nesac\n",
		"direnv": "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A developer's real mise must never be driven by unrelated tests.
	realMise := miseExecutable
	miseExecutable = func() (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { miseExecutable = realMise })
	return home
}

func captureOnboarding(t *testing.T, run func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	result := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		result <- string(data)
	}()
	err = run()
	w.Close()
	output := <-result
	r.Close()
	if err != nil {
		t.Fatalf("command failed: %v\n%s", err, output)
	}
	return output
}

func TestSetupShortAliasOptInAndPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, initial, want              string
		optIn, missingExtension, blocked bool
	}{
		{name: "no opt in", initial: "{}\n", want: "{}\n"},
		{name: "create", initial: "{}\n", want: "aa: autoaccount\n", optIn: true},
		{name: "identical", initial: "aa: autoaccount\n", want: "aa: autoaccount\n", optIn: true},
		{name: "collision", initial: "aa: dangerous\n", want: "aa: dangerous\n", optIn: true},
		{name: "missing extension", initial: "{}\n", want: "{}\n", optIn: true, missingExtension: true},
		{name: "managed envrc", initial: "{}\n", want: "{}\n", optIn: true, blocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := onboardingHome(t)
			aliases := filepath.Join(home, "aliases")
			log := filepath.Join(home, "alias-log")
			if err := os.WriteFile(aliases, []byte(tc.initial), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_ALIASES", aliases)
			t.Setenv("TEST_ALIAS_LOG", log)
			if tc.missingExtension {
				t.Setenv("TEST_MISSING_EXTENSION", "1")
			}
			script := `#!/bin/sh
case "$*" in
  --version) printf 'gh version 2.50.0\n' ;;
  'auth status') printf 'github.com\n  Logged in to github.com account alice (keyring)\n' ;;
  'alias list') printf 'list\n' >> "$TEST_ALIAS_LOG"; cat "$TEST_ALIASES" ;;
  'alias set aa autoaccount') printf 'set\n' >> "$TEST_ALIAS_LOG"; [ -z "${TEST_MISSING_EXTENSION:-}" ] || exit 1; printf 'aa: autoaccount\n' > "$TEST_ALIASES" ;;
  *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(home, "bin", "gh"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if tc.blocked {
				dir := filepath.Join(home, "work")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "alice"}}}); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, ".envrc"), []byte("# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := NewSetupCmd()
			var out, errout bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&errout)
			args := []string{"--native", "--no-shell-rc"}
			if tc.optIn {
				args = append(args, "--alias")
			}
			command.SetArgs(args)
			err := command.Execute()
			if tc.blocked != (err != nil) {
				t.Fatalf("setup error: %v", err)
			}
			got, err := os.ReadFile(aliases)
			if err != nil || string(got) != tc.want {
				t.Fatalf("alias = %q, err %v", got, err)
			}
			if !tc.optIn || tc.blocked {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("unexpected alias operation: %v", err)
				}
			} else if tc.name == "collision" || tc.missingExtension {
				if !strings.Contains(errout.String(), "use gh autoaccount directly") {
					t.Fatalf("missing warning: %q", errout.String())
				}
			} else if tc.name == "identical" && !strings.Contains(out.String(), "already configured") {
				t.Fatalf("missing identical notice: %q", out.String())
			} else if tc.name == "create" && !strings.Contains(out.String(), "Configured gh aa") {
				t.Fatalf("missing created notice: %q", out.String())
			}
		})
	}
}

func TestSetupRestoresShellRCOnLaterFailure(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprintf("symlink=%t", symlink), func(t *testing.T) {
			home := onboardingHome(t)
			rc := filepath.Join(home, ".bashrc")
			target := rc
			if symlink {
				target = filepath.Join(home, "shell-config")
				if err := os.Symlink("shell-config", rc); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(target, []byte("# existing rc\neval \"$(direnv hook bash)\"\n"), 0640); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSettings(config.Settings{}); err != nil {
				t.Fatal(err)
			}
			lib, err := direnvlib.ShellLibPath()
			if err != nil {
				t.Fatal(err)
			}
			hook, err := direnvlib.ShellHookPath()
			if err != nil {
				t.Fatal(err)
			}
			helper, err := gitcred.HelperPath()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{lib, hook, helper} {
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("prior "+filepath.Base(path)), 0640); err != nil {
					t.Fatal(err)
				}
			}
			settings := filepath.Join(home, "config", "gh-autoprofile", "settings.yml")
			before := make(map[string]setupFileSnapshot)
			for _, path := range []string{target, lib, hook, settings, helper} {
				before[path], err = snapshotSetupFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			command := NewSetupCmd()
			command.SetArgs([]string{"--credential-helper"})
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "refusing to overwrite unmanaged") {
				t.Fatalf("expected unmanaged helper failure: %v", err)
			}
			for path, want := range before {
				got, err := snapshotSetupFile(path)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s not restored: got %+v want %+v: %v", path, got, want, err)
				}
			}
			if symlink {
				link, err := os.Readlink(rc)
				if err != nil || link != "shell-config" {
					t.Fatalf("symlink changed: %q %v", link, err)
				}
			}
		})
	}
}

func TestSetupGitIdentityPublishesOnce(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	home := onboardingHome(t)
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", GitName: "Alice", GitEmail: "alice@example.test"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(home, "identity-publication")
	t.Setenv("IDENTITY_PUBLICATION", count)
	wrapper := fmt.Sprintf(`#!/bin/sh
if [ "$1" = config ] && [ "$2" = --file ] && [ "$3" = "$GIT_CONFIG_GLOBAL.lock" ] && [ "$4" = --fixed-value ]; then
  if [ -e "$IDENTITY_PUBLICATION" ]; then exit 91; fi
  printf first > "$IDENTITY_PUBLICATION"
fi
exec %q "$@"
`, realGit)
	if err := os.WriteFile(filepath.Join(home, "bin", "git"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	command := NewSetupCmd()
	command.SetArgs([]string{"--no-shell-rc", "--git-identity"})
	captureOnboarding(t, command.Execute)
	if data, err := os.ReadFile(count); err != nil || string(data) != "first" {
		t.Fatalf("publication count: %q %v", data, err)
	}
	settings, err := config.LoadSettings()
	if err != nil || !settings.GitIdentity || !gitidentity.CheckRegistered() {
		t.Fatalf("identity not registered: %+v %v", settings, err)
	}
}

func TestNativeSetupWithoutDirenvAndRollback(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git unavailable")
	}
	home := onboardingHome(t)
	bin := filepath.Join(home, "bin")
	if err := os.Remove(filepath.Join(bin, "direnv")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, flags := range [][]string{{"--native", "--no-shell-rc"}, {}, {"--migrate"}, {"--gh-shim"}} {
		command := NewSetupCmd()
		command.SetArgs(flags)
		captureOnboarding(t, command.Execute)
		settings, err := config.LoadSettings()
		if err != nil || !settings.Native || !settings.ExternalShell || settings.GitIdentity || !shims.IsNativeGHInstalled() {
			t.Fatalf("native selection lost: %+v, %v", settings, err)
		}
	}
	doctor := NewDoctorCmd()
	doctor.SetArgs([]string{"--fix"})
	captureOnboarding(t, doctor.Execute)
	settings, err := config.LoadSettings()
	if err != nil || !settings.Native || !settings.ExternalShell || !shims.IsNativeGHInstalled() {
		t.Fatalf("doctor --fix lost native selection: %+v, %v", settings, err)
	}
	command := NewSetupCmd()
	command.SetArgs([]string{"--native=false"})
	captureOnboarding(t, command.Execute)
	settings, err = config.LoadSettings()
	if err != nil || settings.Native || !settings.ExternalShell || shims.IsNativeGHInstalled() || !shims.IsGHInstalled() {
		t.Fatalf("rollback failed: %+v, %v", settings, err)
	}
	if !gitcred.CheckConfigured() || !gitcred.IsInstalled() {
		t.Fatal("rollback unexpectedly removed the Git credential helper")
	}
}

func TestNativeSetupFailuresDoNotPersistMode(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	for _, failure := range []string{"proxy", "helper"} {
		t.Run(failure, func(t *testing.T) {
			home := onboardingHome(t)
			if failure == "proxy" {
				path, err := shims.GHPath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("unmanaged"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("GIT_CONFIG_GLOBAL", home)
			}
			command := NewSetupCmd()
			command.SetArgs([]string{"--native", "--no-shell-rc"})
			if err := command.Execute(); err == nil {
				t.Fatal("expected installation failure")
			}
			settings, err := config.LoadSettings()
			if err != nil || settings.Native {
				t.Fatalf("failed setup persisted native mode: %+v, %v", settings, err)
			}
			if _, err := os.Stat(filepath.Join(home, "config", "gh-autoprofile", "hook.sh")); !os.IsNotExist(err) {
				t.Fatalf("failed setup installed hook: %v", err)
			}
		})
	}
}

func TestNativeRollbackRetainsExistingHelper(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	onboardingHome(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gitcred.Install(exe, filepath.Join(os.Getenv("HOME"), "bin", "gh")); err != nil {
		t.Fatal(err)
	}
	if err := gitcred.ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	for _, flags := range [][]string{{"--native", "--no-shell-rc"}, {"--native=false"}} {
		command := NewSetupCmd()
		command.SetArgs(flags)
		captureOnboarding(t, command.Execute)
	}
	if !gitcred.IsInstalled() || !gitcred.CheckConfigured() {
		t.Fatal("rollback removed previously configured helper")
	}
}

func TestNativeTransitionRestoresRequiredAssetsOnWriteFailure(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	for _, prior := range []string{"missing", "legacy", "native", "native-rollback"} {
		for _, failing := range []string{"hook", "library"} {
			t.Run(prior+"/"+failing, func(t *testing.T) {
				home := onboardingHome(t)
				if prior == "legacy" {
					if _, err := shims.InstallGH("/old/extension", "/old/gh"); err != nil {
						t.Fatal(err)
					}
					if err := config.SaveSettings(config.Settings{ExternalShell: true}); err != nil {
						t.Fatal(err)
					}
				} else if strings.HasPrefix(prior, "native") {
					ext := filepath.Join(home, "old-extension")
					if err := os.WriteFile(ext, []byte("#!/bin/sh\n"), 0700); err != nil {
						t.Fatal(err)
					}
					if _, err := shims.InstallNativeGH(ext); err != nil {
						t.Fatal(err)
					}
					if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true}); err != nil {
						t.Fatal(err)
					}
				}
				gitCmd := exec.Command("git", "config", "--global", "--add", "credential.https://github.com.helper", "prior-helper")
				if out, err := gitCmd.CombinedOutput(); err != nil {
					t.Fatalf("git config: %s %v", out, err)
				}
				configDir := filepath.Join(home, "config", "gh-autoprofile")
				if err := os.MkdirAll(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				helper := filepath.Join(configDir, "git-credential-helper")
				if err := os.WriteFile(helper, []byte("#!/usr/bin/env bash\nset -euo pipefail\nexec '/old/extension' __git-credential \"$@\"\n"), 0711); err != nil {
					t.Fatal(err)
				}
				lib, err := direnvlib.ShellLibPath()
				if err != nil {
					t.Fatal(err)
				}
				hook, err := direnvlib.ShellHookPath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(lib), 0700); err != nil {
					t.Fatal(err)
				}
				for name, path := range map[string]string{"hook": hook, "library": lib} {
					if name == failing {
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
					} else if prior != "missing" {
						if err := os.WriteFile(path, []byte("previous "+name+"\n"), 0640); err != nil {
							t.Fatal(err)
						}
					}
				}
				shim, _ := shims.GHPath()
				paths := []string{shim, filepath.Join(configDir, "native-gh-targets.json"), helper, filepath.Join(configDir, "settings.yml"), lib, hook}
				before := make([]setupFileSnapshot, len(paths))
				for i, path := range paths {
					before[i], err = snapshotSetupFile(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				command := NewSetupCmd()
				flag := "--native"
				if prior == "native-rollback" {
					flag = "--native=false"
				}
				command.SetArgs([]string{flag, "--no-shell-rc"})
				if err := command.Execute(); err == nil {
					t.Fatal("expected asset installation failure")
				}
				for i, path := range paths {
					after, err := snapshotSetupFile(path)
					if err != nil || !reflect.DeepEqual(before[i], after) {
						t.Errorf("%s changed: before exists=%t mode=%v bytes=%d; after exists=%t mode=%v bytes=%d; err %v", path, before[i].exists, before[i].mode, len(before[i].data), after.exists, after.mode, len(after.data), err)
					}
				}
				out, err := exec.Command("git", "config", "--global", "--get-all", "credential.https://github.com.helper").Output()
				if err != nil || string(out) != "prior-helper\n" {
					t.Fatalf("helper chain changed: %q %v", out, err)
				}
			})
		}
	}
}

func TestNativeSetupRestoresAllHostHelperChains(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	repo := filepath.Join(home, "enterprise")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{User: "alice", Dir: repo, Host: "ghe.example.com"}}}); err != nil {
		t.Fatal(err)
	}
	hosts := []string{"github.com", "ghe.example.com"}
	for _, host := range hosts {
		for _, value := range []string{"first-" + host, "", "last-" + host} {
			if out, err := exec.Command("git", "config", "--global", "--add", "credential.https://"+host+".helper", value).CombinedOutput(); err != nil {
				t.Fatalf("seed: %s %v", out, err)
			}
		}
	}
	before, err := gitcred.SnapshotHosts(hosts)
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(home, "git-trace")
	t.Setenv("GIT_TRACE", trace)
	hook, err := direnvlib.ShellHookPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hook, 0700); err != nil {
		t.Fatal(err)
	}
	command := NewSetupCmd()
	command.SetArgs([]string{"--native", "--no-shell-rc"})
	if err := command.Execute(); err == nil {
		t.Fatal("expected failure after helper configuration")
	}
	after, err := gitcred.SnapshotHosts(hosts)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("helper chains changed: before %#v after %#v: %v", before, after, err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts {
		if !strings.Contains(string(data), "--add credential.https://"+host+".helper") {
			t.Fatalf("no configuration write for %s: %s", host, data)
		}
	}
}

func TestNativeTransitionFailureRestoresPreviousState(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	for _, prior := range []string{"missing", "legacy", "native"} {
		for _, failure := range []string{"helper-install", "git-config", "git-config-missing"} {
			t.Run(prior+"/"+failure, func(t *testing.T) {
				git, err := exec.LookPath("git")
				if err != nil {
					t.Skip("git unavailable")
				}
				home := onboardingHome(t)
				gitConfig := exec.Command(git, "config", "--global", "--add", "credential.https://github.com.helper", "prior-helper")
				if out, err := gitConfig.CombinedOutput(); err != nil {
					t.Fatalf("initial config: %s %v", out, err)
				}
				shim, _ := shims.GHPath()
				if prior == "legacy" {
					if _, err := shims.InstallGH("/previous/extension", "/previous/gh"); err != nil {
						t.Fatal(err)
					}
				}
				if prior == "native" {
					ext := filepath.Join(home, "old-extension")
					if err := os.WriteFile(ext, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
						t.Fatal(err)
					}
					if _, err := shims.InstallNativeGH(ext); err != nil {
						t.Fatal(err)
					}
					if err := config.SaveSettings(config.Settings{Native: true}); err != nil {
						t.Fatal(err)
					}
				}
				before, beforeErr := os.ReadFile(shim)
				beforeInfo, _ := os.Lstat(shim)
				oldTarget, _ := os.Readlink(shim)
				record := filepath.Join(home, "config", "gh-autoprofile", "native-gh-targets.json")
				oldRecord, oldRecordErr := os.ReadFile(record)
				oldRecordInfo, _ := os.Lstat(record)
				helper, _ := gitcred.HelperPath()
				if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
					t.Fatal(err)
				}
				if failure == "helper-install" {
					if err := os.Mkdir(helper, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					if failure == "git-config" {
						if err := os.WriteFile(helper, []byte("#!/usr/bin/env bash\nset -euo pipefail\nexec '/old/extension' __git-credential \"$@\"\n"), 0711); err != nil {
							t.Fatal(err)
						}
					}
					marker := filepath.Join(home, "fail-once")
					wrapper := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *' --add '* ) if [ ! -e '%s' ]; then : > '%s'; exit 1; fi;; esac\nexec '%s' \"$@\"\n", marker, marker, git)
					if err := os.WriteFile(filepath.Join(home, "bin", "git"), []byte(wrapper), 0700); err != nil {
						t.Fatal(err)
					}
				}
				command := NewSetupCmd()
				command.SetArgs([]string{"--native", "--no-shell-rc"})
				if err := command.Execute(); err == nil {
					t.Fatal("expected failure")
				}
				after, afterErr := os.ReadFile(shim)
				if (beforeErr == nil) != (afterErr == nil) || string(after) != string(before) {
					t.Fatalf("shim changed: before length %d (%v), after length %d (%v)", len(before), beforeErr, len(after), afterErr)
				}
				if beforeInfo != nil {
					info, err := os.Lstat(shim)
					if err != nil || info.Mode() != beforeInfo.Mode() {
						t.Fatalf("shim mode changed: %v %v", info, err)
					}
				}
				newRecord, newRecordErr := os.ReadFile(record)
				if (oldRecordErr == nil) != (newRecordErr == nil) || string(oldRecord) != string(newRecord) {
					t.Fatalf("ownership record changed: %v %v", oldRecordErr, newRecordErr)
				}
				if oldRecordInfo != nil {
					info, err := os.Lstat(record)
					if err != nil || info.Mode() != oldRecordInfo.Mode() {
						t.Fatalf("record mode changed: %v %v", info, err)
					}
				}
				if prior == "native" {
					if target, err := os.Readlink(shim); err != nil || target != oldTarget {
						t.Fatalf("native link changed: %q %v", target, err)
					}
				}
				settings, err := config.LoadSettings()
				if err != nil || settings.Native != (prior == "native") {
					t.Fatalf("selection changed: %+v %v", settings, err)
				}
				if failure == "git-config" {
					data, err := os.ReadFile(helper)
					info, statErr := os.Stat(helper)
					if err != nil || statErr != nil || string(data) != "#!/usr/bin/env bash\nset -euo pipefail\nexec '/old/extension' __git-credential \"$@\"\n" || info.Mode().Perm() != 0711 {
						t.Fatalf("helper changed: %q %v %v", data, err, statErr)
					}
				}
				if failure == "git-config-missing" {
					if _, err := os.Lstat(helper); !os.IsNotExist(err) {
						t.Fatalf("new helper left behind: %v", err)
					}
				}
				check := exec.Command(git, "config", "--global", "--get-all", "credential.https://github.com.helper")
				out, err := check.Output()
				if err != nil || string(out) != "prior-helper\n" {
					t.Fatalf("helper chain changed: %q %v", out, err)
				}
			})
		}
	}
}

func TestExecManagedIdentityUsesGitConfig(t *testing.T) {
	home := onboardingHome(t)
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", GitName: "Alice", GitEmail: "alice@example.test"}}}
	if err := gitidentity.Install(registry); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "--quiet", home).CombinedOutput(); err != nil {
		t.Fatalf("init: %s: %v", out, err)
	}
	for _, override := range []string{"", "explicit@example.test"} {
		t.Setenv("GIT_AUTHOR_EMAIL", override)
		if override == "" {
			os.Unsetenv("GIT_AUTHOR_EMAIL")
		}
		env, err := buildExecEnv(&execContext{Dir: home, Pin: &registry.Pins[0], Token: "test-token"})
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("git", "var", "GIT_AUTHOR_IDENT")
		command.Dir, command.Env = home, env
		out, err := command.CombinedOutput()
		want := "alice@example.test"
		if override != "" {
			want = override
		}
		if err != nil || !strings.Contains(string(out), "Alice <"+want+">") {
			t.Fatalf("identity: %s: %v", out, err)
		}
	}
}

func TestExternalShellSetupMigration(t *testing.T) {
	for _, existingRC := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent RC", true: "managed RC"}[existingRC], func(t *testing.T) {
			home := onboardingHome(t)
			rc := filepath.Join(home, ".bashrc")
			original := "# externally owned\neval \"$(direnv hook bash)\"\n# gh-autoprofile-hook:start\nsource external-hook\n# gh-autoprofile-hook:end\n"
			if existingRC {
				if err := os.WriteFile(rc, []byte(original), 0600); err != nil {
					t.Fatal(err)
				}
			}
			pathBefore := os.Getenv("PATH")
			for _, flags := range [][]string{{"--no-shell-rc", "--gh-shim", "--native=false"}, {"--migrate"}, {}} {
				cmd := NewSetupCmd()
				cmd.SetArgs(flags)
				out := captureOnboarding(t, cmd.Execute)
				if !strings.Contains(out, "integration not verified") {
					t.Fatalf("missing external diagnostic: %s", out)
				}
			}
			doctor := NewDoctorCmd()
			doctor.SetArgs([]string{"--fix"})
			captureOnboarding(t, doctor.Execute)
			settings, err := config.LoadSettings()
			if err != nil || !settings.ExternalShell {
				t.Fatalf("external setting not persisted: %+v %v", settings, err)
			}
			data, err := os.ReadFile(rc)
			if existingRC && (err != nil || string(data) != original) {
				t.Fatalf("RC changed: %s %v", data, err)
			}
			if !existingRC && !os.IsNotExist(err) {
				t.Fatal("RC created in external mode")
			}
			if os.Getenv("PATH") != pathBefore {
				t.Fatal("PATH changed")
			}
			for _, check := range []func() error{NewDoctorCmd().Execute, NewStatusCmd().Execute} {
				out := captureOnboarding(t, check)
				if !strings.Contains(out, "not verified") || strings.Contains(out, "Doctor check passed") || strings.Contains(out, "OK   shell hook") || strings.Contains(out, "shim PATH configured: yes") || strings.Contains(out, "WARN git credential helper") {
					t.Fatalf("misleading diagnostics: %s", out)
				}
			}
			var warnings bytes.Buffer
			root := NewRootCmd()
			root.SetErr(&warnings)
			warnUpgradeDrift(root)
			if warnings.Len() != 0 {
				t.Fatalf("external RC caused migration warning: %s", warnings.String())
			}
			cmd := NewSetupCmd()
			cmd.SetArgs([]string{"--no-shell-rc=false"})
			captureOnboarding(t, cmd.Execute)
			settings, _ = config.LoadSettings()
			if settings.ExternalShell {
				t.Fatal("explicit false did not restore automatic RC mode")
			}
		})
	}
}

// requireAutoAllowEnvironment skips where direnv auto-allow fails closed by
// design for the test's temporary paths: a filesystem whose ACLs cannot be
// inspected (overlayfs, ZFS) or group-writable directories (umask 002). CI sets
// GH_AUTOPROFILE_REQUIRE_DIRENV_2382=1, which turns the skip into a failure.
// Call it before onboardingHome, which then replaces the probe's environment.
func requireAutoAllowEnvironment(t *testing.T) {
	t.Helper()
	probe := t.TempDir()
	bin, repo := filepath.Join(probe, "bin"), filepath.Join(probe, "repo")
	for _, dir := range []string{bin, repo} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "direnv"), []byte("#!/bin/sh\n[ \"$1\" = version ] && printf '2.38.2\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	data := []byte("# gh-autoprofile:start\nexport AUTO_ALLOW_PROBE=1\n# gh-autoprofile:end\n")
	if err := os.WriteFile(filepath.Join(repo, ".envrc"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", probe)
	t.Setenv("XDG_DATA_HOME", filepath.Join(probe, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(probe, "config"))
	t.Setenv("DIRENV_CONFIG", filepath.Join(probe, "direnv"))
	t.Setenv("DIRENV_REQUIRED", "")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := direnvlib.AllowSnapshot(repo, data)
	if err == nil || !strings.Contains(err.Error(), "unsupported filesystem for ACL inspection") && !strings.Contains(err.Error(), "unsafe data path component") {
		return
	}
	if os.Getenv("GH_AUTOPROFILE_REQUIRE_DIRENV_2382") == "1" {
		t.Fatalf("auto-allow must work in this environment: %v", err)
	}
	t.Skipf("direnv auto-allow fails closed here by design: %v", err)
}

func TestRunMigrationRequiresReviewForUnmanagedEnvrc(t *testing.T) {
	requireAutoAllowEnvironment(t)
	home := onboardingHome(t)
	managed := filepath.Join(home, "managed")
	unmanaged := filepath.Join(home, "unmanaged")
	for _, dir := range []string{managed, unmanaged} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{
		{Dir: managed, User: "alice"},
		{Dir: unmanaged, User: "alice"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := direnvlib.WriteEnvrc(config.Pin{Dir: managed, User: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unmanaged, ".envrc"), []byte("export CUSTOM=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := direnvlib.WriteEnvrc(config.Pin{Dir: unmanaged, User: "alice"}); err != nil {
		t.Fatal(err)
	}

	direnvScript := "#!/bin/sh\n[ \"$1\" = version ] && printf '2.38.2\\n'\n"
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte(direnvScript), 0700); err != nil {
		t.Fatal(err)
	}

	report, err := runMigration()
	if err != nil {
		t.Fatal(err)
	}
	if report.EnvrcRewritten != 2 || report.Allowed != 1 || report.Warnings != 1 {
		t.Fatalf("unexpected migration report: %+v", report)
	}
	wantReview := filepath.Join(unmanaged, ".envrc")
	if len(report.ReviewRequired) != 1 || report.ReviewRequired[0] != wantReview {
		t.Fatalf("review list = %v, want %s", report.ReviewRequired, wantReview)
	}
	entries, err := os.ReadDir(filepath.Join(home, "data", "direnv", "allow"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("migration approved unexpected files: %v", entries)
	}
	entry, err := os.ReadFile(filepath.Join(home, "data", "direnv", "allow", entries[0].Name()))
	if err != nil || string(entry) != filepath.Join(managed, ".envrc")+"\n" {
		t.Fatalf("migration approved the wrong file: %q: %v", entry, err)
	}
}

func TestAutoAllowCommandsRejectSharedCheckout(t *testing.T) {
	for _, operation := range []string{"pin", "setup --migrate"} {
		for _, tc := range []struct {
			name      string
			dirPerm   os.FileMode
			envrcPerm os.FileMode
			wantAllow bool
		}{
			{name: "owner only", dirPerm: 0700, envrcPerm: 0600, wantAllow: true},
			{name: "shared directory", dirPerm: 0770, envrcPerm: 0600},
			{name: "initially shared envrc", dirPerm: 0700, envrcPerm: 0664},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				requireAutoAllowEnvironment(t)
				home := onboardingHome(t)
				if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
					t.Fatal(err)
				}
				repo := filepath.Join(home, "repo")
				if err := os.Mkdir(repo, 0700); err != nil {
					t.Fatal(err)
				}
				pin := config.Pin{Dir: repo, User: "alice"}
				if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{pin}}); err != nil {
					t.Fatal(err)
				}
				if err := direnvlib.WriteEnvrc(pin); err != nil {
					t.Fatal(err)
				}
				for path, mode := range map[string]os.FileMode{repo: tc.dirPerm, filepath.Join(repo, ".envrc"): tc.envrcPerm} {
					if err := os.Chmod(path, mode); err != nil {
						t.Fatal(err)
					}
				}
				direnvScript := "#!/bin/sh\n[ \"$1\" = version ] && printf '2.38.2\\n'\n"
				if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte(direnvScript), 0700); err != nil {
					t.Fatal(err)
				}
				command := NewPinCmd()
				command.SetArgs([]string{"alice", "--dir", repo})
				if operation == "setup --migrate" {
					command = NewSetupCmd()
					command.SetArgs([]string{"--migrate", "--no-shell-rc"})
				}
				out := captureOnboarding(t, command.Execute)
				data, err := os.ReadDir(filepath.Join(home, "data", "direnv", "allow"))
				if tc.wantAllow {
					if err != nil || len(data) != 1 {
						t.Fatalf("owner-only checkout not allowed: %v %v\n%s", data, err, out)
					}
				} else {
					if err == nil {
						t.Fatalf("mutable checkout auto-authorized entries: %v", data)
					}
					if !os.IsNotExist(err) || !strings.Contains(out, "review") {
						t.Fatalf("missing manual review: %v\n%s", err, out)
					}
				}
			})
		}
	}
}

func TestDoctorWarnsWhenShimTargetIsStale(t *testing.T) {
	home := onboardingHome(t)
	if _, err := shims.InstallGH("/tmp/gh-autoprofile", "/old/gh"); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "gh shim target is stale") || !strings.Contains(out, "setup --migrate") {
		t.Fatalf("missing stale shim warning: %s", out)
	}

	currentGH := filepath.Join(home, "bin", "gh")
	if _, err := shims.InstallGH("/tmp/gh-autoprofile", currentGH); err != nil {
		t.Fatal(err)
	}
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "gh shim target matches current gh") || strings.Contains(out, "gh shim target is stale") {
		t.Fatalf("current shim reported stale: %s", out)
	}
}

func TestRunPinOnlyAutoAllowsManagedEnvrc(t *testing.T) {
	for _, tc := range []struct {
		name          string
		customContent bool
		wantAllow     bool
	}{
		{name: "managed only", wantAllow: true},
		{name: "unmanaged content", customContent: true, wantAllow: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireAutoAllowEnvironment(t)
			home := onboardingHome(t)
			if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(home, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			if tc.customContent {
				if err := os.WriteFile(filepath.Join(repo, ".envrc"), []byte("export CUSTOM=1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			direnvScript := "#!/bin/sh\n[ \"$1\" = version ] && printf '2.38.2\\n'\n"
			if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte(direnvScript), 0700); err != nil {
				t.Fatal(err)
			}

			output := captureOnboarding(t, func() error {
				return runPin("alice", repo, "", "", "", false)
			})
			entries, err := os.ReadDir(filepath.Join(home, "data", "direnv", "allow"))
			if tc.wantAllow {
				if err != nil || len(entries) != 1 {
					t.Fatalf("managed .envrc was not allowed: %v, %v", entries, err)
				}
			} else {
				if err == nil {
					t.Fatalf("unmanaged .envrc was allowed: %v", entries)
				}
				if !strings.Contains(output, "contains unmanaged content") {
					t.Fatalf("missing manual review warning: %s", output)
				}
			}
		})
	}
}

func TestManagedIdentityOnboarding(t *testing.T) {
	home := onboardingHome(t)
	repo := filepath.Join(home, "repo")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = home
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return string(out)
	}
	git("init", "--quiet", repo)
	git("config", "--global", "user.name", "Default")
	git("config", "--global", "user.email", "default@example.test")
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: repo, User: "alice", GitName: "Alice", GitEmail: "alice@example.test"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := direnvlib.WriteEnvrc(registry.Pins[0]); err != nil {
		t.Fatal(err)
	}
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--no-shell-rc", "--git-identity"})
	captureOnboarding(t, setup.Execute)
	if !gitidentity.CheckRegistered() {
		t.Fatal("identity not registered")
	}
	data, _ := os.ReadFile(filepath.Join(repo, ".envrc"))
	if strings.Contains(string(data), "alice@example.test") {
		t.Fatal("setup did not migrate legacy environment identity")
	}
	if got := git("-C", repo, "var", "GIT_AUTHOR_IDENT"); !strings.HasPrefix(got, "Alice <alice@example.test>") {
		t.Fatal(got)
	}
	captureOnboarding(t, func() error { return runPin("alice", repo, "updated@example.test", "Updated", "", false) })
	if got := git("-C", repo, "var", "GIT_COMMITTER_IDENT"); !strings.HasPrefix(got, "Updated <updated@example.test>") {
		t.Fatal(got)
	}
	registry, _ = config.LoadPins()
	registry.Pins[0].GitName = "Migrated"
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	doctor := NewDoctorCmd()
	doctor.SetArgs([]string{"--fix"})
	captureOnboarding(t, doctor.Execute)
	if got := git("-C", repo, "var", "GIT_AUTHOR_IDENT"); !strings.HasPrefix(got, "Migrated <updated@example.test>") {
		t.Fatal(got)
	}
	unpin := NewUnpinCmd()
	unpin.SetArgs([]string{repo})
	captureOnboarding(t, unpin.Execute)
	if got := git("-C", repo, "var", "GIT_AUTHOR_IDENT"); !strings.HasPrefix(got, "Default <default@example.test>") {
		t.Fatal(got)
	}
}

func TestFailedIdentityEnablePreservesLegacyIdentity(t *testing.T) {
	home := onboardingHome(t)
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", GitName: "Legacy", GitEmail: "legacy@example.test"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := direnvlib.WriteEnvrc(registry.Pins[0]); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(home, ".envrc"))
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	if err := os.WriteFile(global+".lock", []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--no-shell-rc", "--git-identity"})
	if err := setup.Execute(); err == nil {
		t.Fatal("failed enable returned success")
	}
	settings, err := config.LoadSettings()
	if err != nil || settings.GitIdentity || settings.ExternalShell {
		t.Fatalf("failed enable did not restore previous settings: %+v %v", settings, err)
	}
	after, _ := os.ReadFile(filepath.Join(home, ".envrc"))
	if string(before) != string(after) {
		t.Fatal("failed enable changed legacy envrc")
	}
	if err := direnvlib.WriteEnvrc(registry.Pins[0]); err != nil {
		t.Fatal(err)
	}
	after, _ = os.ReadFile(filepath.Join(home, ".envrc"))
	if !strings.Contains(string(after), "legacy@example.test") {
		t.Fatal("later envrc write stripped legacy identity")
	}
	if err := os.Remove(global + ".lock"); err != nil {
		t.Fatal(err)
	}
	setup = NewSetupCmd()
	setup.SetArgs([]string{"--git-identity"})
	captureOnboarding(t, setup.Execute)
	settings, _ = config.LoadSettings()
	if !settings.GitIdentity || !gitidentity.CheckRegistered() {
		t.Fatal("retry did not enable identity")
	}
}

func TestLockedGlobalPinUnpinAndMigrationAreRetryable(t *testing.T) {
	home := onboardingHome(t)
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--no-shell-rc", "--git-identity", "--native=false"})
	captureOnboarding(t, setup.Execute)
	captureOnboarding(t, func() error { return runPin("alice", repo, "original@example.test", "Original", "", false) })
	pinsPath, _ := config.PinsFilePath()
	includePath, _ := gitidentity.Path()
	paths := []string{pinsPath, includePath, filepath.Join(repo, ".envrc"), os.Getenv("GIT_CONFIG_GLOBAL")}
	before := make(map[string]string)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = string(data)
	}
	log := filepath.Join(home, "direnv-calls")
	t.Setenv("DIRENV_TEST_LOG", log)
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$DIRENV_TEST_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	lock := os.Getenv("GIT_CONFIG_GLOBAL") + ".lock"
	if err := os.WriteFile(lock, []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(repo, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func() error{
		func() error { return runPin("alice", repo, "changed@example.test", "Changed", "", false) },
		func() error { return runPin("alice", child, "child@example.test", "Child", "", false) },
		func() error { return runUnpin(NewUnpinCmd(), []string{repo}) },
		func() error {
			doctor := NewDoctorCmd()
			doctor.SetArgs([]string{"--fix"})
			return doctor.Execute()
		},
	} {
		if err := run(); err == nil {
			t.Fatal("locked global operation returned success")
		}
		for path, expected := range before {
			data, err := os.ReadFile(path)
			if err != nil || string(data) != expected {
				t.Fatalf("failed operation changed %s: %s %v", path, data, err)
			}
		}
	}
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "deny") || strings.Contains(string(data), "allow") {
		t.Fatalf("failed cleanup must revoke without reauthorizing: %s", data)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	captureOnboarding(t, func() error { return runPin("alice", repo, "changed@example.test", "Changed", "", false) })
	captureOnboarding(t, func() error { return runUnpin(NewUnpinCmd(), []string{repo}) })
	registry, _ := config.LoadPins()
	if len(registry.Pins) != 0 {
		t.Fatal("retry did not remove pin")
	}
	if _, err := os.Stat(filepath.Join(repo, ".envrc")); !os.IsNotExist(err) {
		t.Fatal("retry skipped envrc cleanup")
	}
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "deny") {
		t.Fatal("retry skipped direnv deny")
	}
}

func TestUnpinRequiresExactPin(t *testing.T) {
	home := onboardingHome(t)
	child := filepath.Join(home, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, ".envrc"), []byte("# child owned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runUnpin(NewUnpinCmd(), []string{child}); err == nil {
		t.Fatal("unpin accepted inherited pin")
	}
	registry, _ = config.LoadPins()
	data, _ := os.ReadFile(filepath.Join(child, ".envrc"))
	if len(registry.Pins) != 1 || registry.Pins[0].Dir != home || string(data) != "# child owned\n" {
		t.Fatal("inherited unpin modified state")
	}
}

func TestOnboardingMutationLock(t *testing.T) {
	home := onboardingHome(t)
	if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	unlock, err := config.LockMutations()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if unlock != nil {
			unlock()
		}
	})
	for _, run := range []func() error{
		func() error { return runPin("alice", home, "alice@example.test", "Alice", "", false) },
		func() error { return runUnpin(NewUnpinCmd(), []string{home}) },
		NewSetupCmd().Execute,
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "cannot lock onboarding state") {
			t.Fatalf("mutation bypassed lock: %v", err)
		}
	}
	path, _ := config.PinsFilePath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("concurrent command wrote registry")
	}
	unlock()
	unlock = nil
	captureOnboarding(t, func() error { return runPin("alice", home, "alice@example.test", "Alice", "", false) })
}

func TestUnpinCleanupFailureRetainsPin(t *testing.T) {
	home := onboardingHome(t)
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--no-shell-rc", "--git-identity", "--native=false"})
	captureOnboarding(t, setup.Execute)
	captureOnboarding(t, func() error { return runPin("alice", home, "alice@example.test", "Alice", "", false) })
	path, _ := config.PinsFilePath()
	before, _ := os.ReadFile(path)
	includePath, _ := gitidentity.Path()
	includeBefore, _ := os.ReadFile(includePath)
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runUnpin(NewUnpinCmd(), []string{home}); err == nil {
		t.Fatal("failed cleanup returned success")
	}
	after, _ := os.ReadFile(path)
	includeAfter, _ := os.ReadFile(includePath)
	if string(before) != string(after) || string(includeBefore) != string(includeAfter) {
		t.Fatal("failed cleanup lost pin/identity")
	}
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	captureOnboarding(t, func() error { return runUnpin(NewUnpinCmd(), []string{home}) })
}

func TestUnpinMissingResources(t *testing.T) {
	for _, missing := range []string{"envrc", "directory"} {
		t.Run(missing, func(t *testing.T) {
			home := onboardingHome(t)
			repo := filepath.Join(home, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			setup := NewSetupCmd()
			setup.SetArgs([]string{"--no-shell-rc", "--git-identity", "--native=false"})
			captureOnboarding(t, setup.Execute)
			captureOnboarding(t, func() error { return runPin("alice", repo, "alice@example.test", "Alice", "", false) })
			if err := os.Remove(filepath.Join(repo, ".envrc")); err != nil {
				t.Fatal(err)
			}
			if missing == "directory" {
				if err := os.Remove(repo); err != nil {
					t.Fatal(err)
				}
			}
			// An absent resource must not depend on a successful direnv invocation.
			if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			captureOnboarding(t, func() error { return runUnpin(NewUnpinCmd(), []string{repo}) })
			registry, err := config.LoadPins()
			if err != nil || len(registry.Pins) != 0 {
				t.Fatalf("stale pin remains: %+v %v", registry, err)
			}
			include, _ := gitidentity.Path()
			data, err := os.ReadFile(include)
			if err != nil || strings.Contains(string(data), "includeIf") {
				t.Fatalf("stale identity reference remains: %s %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(repo, ".envrc")); !os.IsNotExist(err) {
				t.Fatalf("missing resource recreated: %v", err)
			}
		})
	}
}

func TestPinEnvrcWriteFailureRestoresIdentity(t *testing.T) {
	home := onboardingHome(t)
	repo := filepath.Join(home, "repo")
	if out, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--no-shell-rc", "--git-identity", "--native=false"})
	captureOnboarding(t, setup.Execute)
	captureOnboarding(t, func() error { return runPin("alice", repo, "original@example.test", "Original", "", false) })
	pinsPath, _ := config.PinsFilePath()
	includePath, _ := gitidentity.Path()
	envrc := filepath.Join(repo, ".envrc")
	target := filepath.Join(home, "existing.envrc")
	if err := os.Rename(envrc, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, envrc); err != nil {
		t.Fatal(err)
	}
	before := make(map[string]string)
	for _, path := range []string{pinsPath, includePath, target} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = string(data)
	}
	err := runPin("alice", repo, "changed@example.test", "Changed", "", true)
	if err == nil || !strings.Contains(err.Error(), "cannot write .envrc") {
		t.Fatalf("expected envrc write failure, got: %v", err)
	}
	for path, expected := range before {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != expected {
			t.Fatalf("failed pin changed %s: %s %v", path, data, err)
		}
	}
	if got, err := os.Readlink(envrc); err != nil || got != target {
		t.Fatalf("envrc symlink changed: %q %v", got, err)
	}
	for _, variable := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		out, err := exec.Command("git", "-C", repo, "var", variable).CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), "Original <original@example.test>") {
			t.Fatalf("failed pin changed %s: %s %v", variable, out, err)
		}
	}
}

func TestDoctorWarnsOnLooseConfigPermissionsAndMigrationRepairs(t *testing.T) {
	onboardingHome(t)
	configDir, err := config.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	pinsPath, err := config.PinsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinsPath, []byte("pins: []\n"), 0644); err != nil {
		t.Fatal(err)
	}

	out := captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "config directory") || !strings.Contains(out, "want 0700") {
		t.Fatalf("missing config directory permission warning: %s", out)
	}
	if !strings.Contains(out, "pins file") || !strings.Contains(out, "want 0600") {
		t.Fatalf("missing pins file permission warning: %s", out)
	}
	if strings.Contains(out, "permissions are hardened") {
		t.Fatalf("hardened OK reported despite loose permissions: %s", out)
	}

	report, err := runMigration()
	if err != nil {
		t.Fatalf("runMigration failed: %v", err)
	}
	if len(report.PermRepairsFailed) != 0 {
		t.Fatalf("permission repairs reported failed: %v", report.PermRepairsFailed)
	}
	if fi, err := os.Stat(configDir); err != nil || fi.Mode().Perm() != 0700 {
		t.Fatalf("config directory not repaired to 0700: %v %o", err, fi.Mode().Perm())
	}
	if fi, err := os.Stat(pinsPath); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("pins file not repaired to 0600: %v %o", err, fi.Mode().Perm())
	}

	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "permissions are hardened") {
		t.Fatalf("hardened OK missing after repair: %s", out)
	}
	if strings.Contains(out, "want 0700") || strings.Contains(out, "want 0600") {
		t.Fatalf("stale permission warnings after repair: %s", out)
	}
}
