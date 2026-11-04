package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

func TestPinLeavesEnvrcAndUnpinCleansManagedBlock(t *testing.T) {
	home := onboardingHome(t)
	dir := filepath.Join(home, "repo")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{Native: true}); err != nil {
		t.Fatal(err)
	}
	if err := runPin("alice", dir, "", "", "", false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".envrc")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("native pin created envrc: %v", err)
	}
	const old = "before\r\n# gh-autoprofile:start\r\nuse_gh_autoprofile alice\r\n# gh-autoprofile:end\r\nafter\r\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(home, "calls")
	t.Setenv("TEST_DIRENV_LOG", log)
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nprintf '%s %s\\n' \"$1\" \"$2\" >> \"$TEST_DIRENV_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, func() error { return runPin("alice", dir, "", "", "", false) })
	if !strings.Contains(out, "setup --migrate") {
		t.Fatalf("no pointer to the cleanup command: %s", out)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != old {
		t.Fatalf("pin changed managed bytes: %q %v", data, err)
	}
	if calls, _ := os.ReadFile(log); len(calls) != 0 {
		t.Fatalf("pin revoked authorization: %q", calls)
	}
	if err := runUnpin(NewUnpinCmd(), []string{dir}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "before\r\nafter\r\n" {
		t.Fatalf("unpin did not keep only the unmanaged bytes: %q %v", data, err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "deny "+path+"\n" {
		t.Fatalf("authorization calls: %q", calls)
	}
}
func TestUnpinRefusesUnsafeEnvrcCleanup(t *testing.T) {
	for _, kind := range []string{"malformed", "symlink", "deny"} {
		t.Run(kind, func(t *testing.T) {
			home := onboardingHome(t)
			dir := filepath.Join(home, "repo")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSettings(config.Settings{Native: true}); err != nil {
				t.Fatal(err)
			}
			if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "previous", Host: "github.example.test"}}}); err != nil {
				t.Fatal(err)
			}
			pins, _ := config.PinsFilePath()
			before, _ := os.ReadFile(pins)
			path := filepath.Join(dir, ".envrc")
			content := "# gh-autoprofile:start\nuse_gh_autoprofile previous\n# gh-autoprofile:end\n"
			if kind == "malformed" {
				content = "# gh-autoprofile:start\n"
			}
			writePath := path
			if kind == "symlink" {
				writePath = filepath.Join(home, "target")
			}
			if err := os.WriteFile(writePath, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(writePath, path); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "deny" {
				if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := runUnpin(NewUnpinCmd(), []string{dir}); err == nil {
				t.Fatal("unsafe cleanup accepted")
			}
			after, _ := os.ReadFile(pins)
			if string(after) != string(before) {
				t.Fatal("pin changed")
			}
			after, _ = os.ReadFile(writePath)
			if string(after) != content {
				t.Fatal("envrc changed")
			}
		})
	}
}
func TestUnpinRefusesEnvrcChangedDuringDeny(t *testing.T) {
	home := onboardingHome(t)
	dir := filepath.Join(home, "repo")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{Native: true}); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "previous"}}}); err != nil {
		t.Fatal(err)
	}
	pins, _ := config.PinsFilePath()
	before, _ := os.ReadFile(pins)
	path := filepath.Join(dir, ".envrc")
	const content = "# gh-autoprofile:start\nuse_gh_autoprofile previous\n# gh-autoprofile:end\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nprintf 'concurrent\\n' >> \"$2\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	err := runUnpin(NewUnpinCmd(), []string{dir})
	if err == nil || !strings.Contains(err.Error(), "changed during cleanup") {
		t.Fatalf("missing failure: %v", err)
	}
	after, _ := os.ReadFile(pins)
	if string(after) != string(before) {
		t.Fatal("pin changed")
	}
	after, _ = os.ReadFile(path)
	if string(after) != content+"concurrent\n" {
		t.Fatalf("concurrent change lost: %q", after)
	}
}
func TestUnpinHelperFailureRestoresRegistryAndEnvrc(t *testing.T) {
	home := onboardingHome(t)
	dir := filepath.Join(home, "repo")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{Native: true}); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "previous", Host: "github.example.test"}}}); err != nil {
		t.Fatal(err)
	}
	hosts := []string{"github.com", "github.example.test"}
	if err := gitcred.ConfigureHosts(hosts); err != nil {
		t.Fatal(err)
	}
	beforeHelpers, err := gitcred.SnapshotHosts(hosts)
	if err != nil {
		t.Fatal(err)
	}
	pins, _ := config.PinsFilePath()
	before, _ := os.ReadFile(pins)
	path := filepath.Join(dir, ".envrc")
	const content = "outside\n# gh-autoprofile:start\nuse_gh_autoprofile previous\n# gh-autoprofile:end\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL")+".lock", []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	err = runUnpin(NewUnpinCmd(), []string{dir})
	if err == nil || !strings.Contains(err.Error(), "authorization was revoked") {
		t.Fatalf("missing rollback: %v", err)
	}
	after, _ := os.ReadFile(pins)
	if string(after) != string(before) {
		t.Fatal("registry changed")
	}
	after, _ = os.ReadFile(path)
	if string(after) != content {
		t.Fatal("envrc not restored")
	}
	afterHelpers, err := gitcred.SnapshotHosts(hosts)
	if err != nil {
		t.Fatal(err)
	}
	for host, values := range beforeHelpers {
		if !reflect.DeepEqual(values, afterHelpers[host]) {
			t.Fatalf("helper changed for %s: %v", host, afterHelpers[host])
		}
	}
}
func TestNativeMigrationFailureRollsBackHostHelpersAndIdentity(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	for _, identity := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-identity", true: "existing-identity"}[identity], func(t *testing.T) {
			home := onboardingHome(t)
			first, second := filepath.Join(home, "first"), filepath.Join(home, "second")
			for _, dir := range []string{first, second} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			registry := &config.PinRegistry{Pins: []config.Pin{{Dir: first, User: "alice", GitEmail: "alice@example.test"}, {Dir: second, User: "alice", Host: "github.example.test"}}}
			if err := config.SavePins(registry); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSettings(config.Settings{ExternalShell: true, GitIdentity: identity}); err != nil {
				t.Fatal(err)
			}
			if identity {
				if err := gitidentity.Install(registry); err != nil {
					t.Fatal(err)
				}
			}
			for _, host := range []string{"github.com", "github.example.test"} {
				if out, err := exec.Command("git", "config", "--global", "--add", "credential.https://"+host+".helper", "prior-"+host).CombinedOutput(); err != nil {
					t.Fatalf("git config: %s %v", out, err)
				}
			}
			beforeHelpers, err := gitcred.SnapshotHosts([]string{"github.com", "github.example.test"})
			if err != nil {
				t.Fatal(err)
			}
			pins, _ := config.PinsFilePath()
			beforePins, _ := os.ReadFile(pins)
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			wrapper := "#!/bin/sh\ncase \"$*\" in *'--unset-all include.path'*) exit 1;; esac\nexec " + fmt.Sprintf("%q", realGit) + " \"$@\"\n"
			const good = "outside\n# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\n"
			if err := os.WriteFile(filepath.Join(first, ".envrc"), []byte(good), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(second, ".envrc"), []byte("# gh-autoprofile:start\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "bin", "git"), []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			setup := NewSetupCmd()
			setup.SetArgs([]string{"--git-identity"})
			if err := setup.Execute(); err == nil || !strings.Contains(err.Error(), "migration failed") {
				t.Fatalf("expected migration failure: %v", err)
			}
			settings, err := config.LoadSettings()
			if err != nil || settings.Native || settings.GitIdentity != identity {
				t.Fatalf("settings: %+v %v", settings, err)
			}
			afterPins, _ := os.ReadFile(pins)
			if string(beforePins) != string(afterPins) {
				t.Fatal("pins changed")
			}
			data, _ := os.ReadFile(filepath.Join(first, ".envrc"))
			if string(data) != good {
				t.Fatalf("envrc touched before the commit point: %q", data)
			}
			afterHelpers, err := gitcred.SnapshotHosts([]string{"github.com", "github.example.test"})
			if err != nil {
				t.Fatal(err)
			}
			for host, before := range beforeHelpers {
				if !reflect.DeepEqual(before, afterHelpers[host]) {
					t.Fatalf("helper changed for %s: %v", host, afterHelpers[host])
				}
			}
			if gitidentity.CheckRegistered() != identity {
				t.Fatal("identity registration changed")
			}
		})
	}
}

func TestSetupConvertsExportPinWithoutMigrateFlag(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	dir := filepath.Join(home, "repo")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "alice", Mode: config.ModeExport}}}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".envrc")
	const content = "# gh-autoprofile:start\nuse_gh_autoprofile_export alice\n# gh-autoprofile:end\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	setup := NewSetupCmd()
	if err := setup.Execute(); err != nil {
		t.Fatal(err)
	}
	registry, err := config.LoadPins()
	if err != nil || len(registry.Pins) != 1 || registry.Pins[0].Mode != config.ModeWrapper {
		t.Fatalf("export pin not converted: %+v %v", registry, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("managed-only envrc not removed: %v", err)
	}
}
func TestNativeSetupWithoutMigrationFreshNoDirenv(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	if err := os.Remove(filepath.Join(home, "bin", "direnv")); err != nil {
		t.Fatal(err)
	}
	// Keep Git available without exposing an installed system direnv.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(home, "bin", "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(home, "bin"))
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--native", "--no-shell-rc"})
	if err := setup.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeUnpinInaccessibleDirectoryPreservesState(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permissions")
	}
	home := onboardingHome(t)
	parent := filepath.Join(home, "blocked")
	dir := filepath.Join(parent, "repo")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true, GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "alice", GitEmail: "alice@example.test"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := gitidentity.Install(registry); err != nil {
		t.Fatal(err)
	}
	if _, err := gitcred.Install(filepath.Join(home, "candidate"), filepath.Join(home, "bin", "gh")); err != nil {
		t.Fatal(err)
	}
	if err := gitcred.ConfigureHosts([]string{"github.com"}); err != nil {
		t.Fatal(err)
	}
	const content = "before\n# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\nafter\n"
	path := filepath.Join(dir, ".envrc")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	pins, err := config.PinsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	shim, err := shims.GHPath()
	if err != nil {
		t.Fatal(err)
	}
	helper, err := gitcred.HelperPath()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := gitidentity.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(shim), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte("existing executable\n"), 0711); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(filepath.Dir(pins), "native-gh-targets.json")
	if err := os.WriteFile(record, []byte("existing routing record\n"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := []string{pins, filepath.Join(filepath.Dir(pins), "settings.yml"), shim, record, helper, os.Getenv("GIT_CONFIG_GLOBAL"), identity}
	fragments, err := filepath.Glob(filepath.Join(filepath.Dir(identity), "pin-*.gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, fragments...)
	before := make([]setupFileSnapshot, len(paths))
	for i, p := range paths {
		before[i], err = snapshotSetupFile(p)
		if err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(home, "direnv-calls")
	t.Setenv("TEST_DIRENV_LOG", log)
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$TEST_DIRENV_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0700) })
	if _, err := os.Stat(dir); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("fixture requires EACCES, got %v", err)
	}
	command := NewUnpinCmd()
	command.SetArgs([]string{dir})
	err = command.Execute()
	if chmodErr := os.Chmod(parent, 0700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil || string(data) != content {
		t.Fatalf("managed envrc changed: %v", readErr)
	}
	after, readErr := snapshotSetupFile(pins)
	if readErr != nil {
		t.Fatal(readErr)
	}
	t.Logf("unpin error: %v; registry changed: %t; managed envrc unchanged: %t", err, !reflect.DeepEqual(before[0], after), string(data) == content)
	if err == nil || !errors.Is(err, syscall.EACCES) {
		t.Errorf("expected inaccessible directory error: %v", err)
	}
	for i, p := range paths {
		after, readErr := snapshotSetupFile(p)
		if readErr != nil || !reflect.DeepEqual(before[i], after) {
			t.Errorf("state changed: %s: %v", p, readErr)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Errorf("direnv invoked: %v", err)
	}
}

func TestNativeUnpinMissingDirectoryStillRemovesPin(t *testing.T) {
	home := onboardingHome(t)
	dir := filepath.Join(home, "missing")
	if err := config.SaveSettings(config.Settings{Native: true}); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "alice"}}}); err != nil {
		t.Fatal(err)
	}
	command := NewUnpinCmd()
	command.SetArgs([]string{dir})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	registry, err := config.LoadPins()
	if err != nil || len(registry.Pins) != 0 {
		t.Fatalf("missing pin not removed: %+v %v", registry, err)
	}
}
