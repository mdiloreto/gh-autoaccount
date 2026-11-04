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

func TestNativePinCleanupAndUnpin(t *testing.T) {
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
		t.Fatalf("fresh native pin created envrc: %v", err)
	}
	const old = "before\r\n# gh-autoprofile:start\r\nuse_gh_autoprofile alice\r\n# gh-autoprofile:end\r\nafter\r\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(home, "calls")
	t.Setenv("TEST_DIRENV_LOG", log)
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> \"$TEST_DIRENV_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runPin("alice", dir, "", "", "", false); err == nil || !strings.Contains(err.Error(), "direnv deny") {
		t.Fatalf("expected manual cleanup: %v", err)
	}
	if err := runUnpin(NewUnpinCmd(), []string{dir}); err == nil || !strings.Contains(err.Error(), "direnv deny") {
		t.Fatalf("expected manual cleanup before unpin: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != old {
		t.Fatalf("managed bytes changed: %q %v", data, err)
	}
	if err := os.WriteFile(path, []byte("before\r\nafter\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runPin("alice", dir, "", "", "", false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "before\r\nafter\r\n" {
		t.Fatalf("unmanaged bytes: %q %v", data, err)
	}
	if err := runUnpin(NewUnpinCmd(), []string{dir}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "before\r\nafter\r\n" {
		t.Fatalf("unpin changed unmanaged bytes: %q %v", data, err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "" {
		t.Fatalf("unexpected authorization calls: %q", calls)
	}
}

func TestNativePinRefusesUnsafeCleanup(t *testing.T) {
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
			if err := runPin("alice", dir, "", "", "", false); err == nil {
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

func TestNativePinRefusesEnvrcChangedDuringDeny(t *testing.T) {
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
	err := runPin("alice", dir, "", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "direnv deny") {
		t.Fatalf("missing failure: %v", err)
	}
	after, _ := os.ReadFile(pins)
	if string(after) != string(before) {
		t.Fatal("pin changed")
	}
	after, _ = os.ReadFile(path)
	if string(after) != content {
		t.Fatalf("concurrent change lost: %q", after)
	}
}

func TestNativePinHelperFailureRestoresRegistryAndEnvrc(t *testing.T) {
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
	err = runPin("alice", dir, "", "", "", false)
	if err == nil || !strings.Contains(err.Error(), "direnv deny") {
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
			const good = "outside\n# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\n"
			if err := os.WriteFile(filepath.Join(first, ".envrc"), []byte(good), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(second, ".envrc"), []byte("# gh-autoprofile:start\n"), 0600); err != nil {
				t.Fatal(err)
			}
			setup := NewSetupCmd()
			setup.SetArgs([]string{"--native", "--git-identity"})
			if err := setup.Execute(); err == nil || !strings.Contains(err.Error(), "direnv deny") {
				t.Fatalf("expected cleanup rollback: %v", err)
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
				t.Fatalf("envrc not restored: %q", data)
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

func TestNativeMigrationRollbackPreservesConcurrentEnvrcEdit(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	first, second := filepath.Join(home, "first"), filepath.Join(home, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: first, User: "alice"}, {Dir: second, User: "alice"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	pinsPath, err := config.PinsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(filepath.Dir(pinsPath), "settings.yml")
	beforeSettings, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	beforePins, err := os.ReadFile(pinsPath)
	if err != nil {
		t.Fatal(err)
	}
	const original = "before\n# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\nafter\n"
	firstPath, secondPath := filepath.Join(first, ".envrc"), filepath.Join(second, ".envrc")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, []byte(original), 0600); err != nil {
			t.Fatal(err)
		}
	}
	preEditPath, editedPath, callsPath := filepath.Join(home, "pre-edit"), filepath.Join(home, "edited"), filepath.Join(home, "calls")
	t.Setenv("TEST_FIRST_ENVRC", firstPath)
	t.Setenv("TEST_SECOND_ENVRC", secondPath)
	t.Setenv("TEST_PRE_EDIT", preEditPath)
	t.Setenv("TEST_EDITED", editedPath)
	t.Setenv("TEST_DIRENV_CALLS", callsPath)
	script := `#!/bin/sh
case "$1" in
  version) printf '2.0.0\n'; exit 0 ;;
  deny)
    printf '%s\n' "$2" >> "$TEST_DIRENV_CALLS"
    if [ "$2" = "$TEST_FIRST_ENVRC" ]; then exit 0; fi
    if [ "$2" = "$TEST_SECOND_ENVRC" ]; then
      cp "$TEST_FIRST_ENVRC" "$TEST_PRE_EDIT" || exit 90
      printf '# concurrent-edit-unique-rollback-repro\n' >> "$TEST_FIRST_ENVRC" || exit 91
      cp "$TEST_FIRST_ENVRC" "$TEST_EDITED" || exit 92
      printf 'controlled second deny failure\n' >&2
      exit 42
    fi ;;
esac
exit 93
`
	if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--native", "--migrate"})
	err = setup.Execute()
	if err == nil || !strings.Contains(err.Error(), "direnv deny") {
		t.Fatalf("unexpected setup result: %v", err)
	}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, path := range []string{callsPath, preEditPath, editedPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("editor or deny invoked: %s: %v", path, err)
		}
	}
	after := read(firstPath)
	if got := read(secondPath); got != original {
		t.Fatalf("second .envrc changed: %q", got)
	}
	if got := read(settingsPath); got != string(beforeSettings) {
		t.Fatalf("settings not restored: %q", got)
	}
	if got := read(pinsPath); got != string(beforePins) {
		t.Fatalf("pins changed: %q", got)
	}
	settings, err := config.LoadSettings()
	if err != nil || settings.Native || !settings.ExternalShell {
		t.Fatalf("settings state: %+v %v", settings, err)
	}
	if after != original {
		t.Errorf("original managed bytes changed: %q", after)
	}
}

func TestNativeSetupWithoutMigrationRefusesManagedEnvrc(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	for _, existing := range []bool{false, true} {
		for _, implicit := range []bool{false, true} {
			for _, malformed := range []bool{false, true} {
				t.Run(fmt.Sprintf("existing=%t/implicit=%t/malformed=%t", existing, implicit, malformed), func(t *testing.T) {
					home := onboardingHome(t)
					dir := filepath.Join(home, "repo")
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: dir, User: "alice"}}}); err != nil {
						t.Fatal(err)
					}
					if err := config.SaveSettings(config.Settings{ExternalShell: true, Native: implicit}); err != nil {
						t.Fatal(err)
					}
					if existing {
						setup := NewSetupCmd()
						setup.SetArgs([]string{"--native"})
						if err := setup.Execute(); err != nil {
							t.Fatal(err)
						}
						if err := config.SaveSettings(config.Settings{ExternalShell: true, Native: implicit}); err != nil {
							t.Fatal(err)
						}
					}
					path := filepath.Join(dir, ".envrc")
					managed := "before\r\n# gh-autoprofile:start\r\nuse_gh_autoprofile alice\r\n# gh-autoprofile:end\r\nafter\r\n"
					if malformed {
						managed = "before\n# gh-autoprofile:start\n"
					}
					if err := os.WriteFile(path, []byte(managed), 0600); err != nil {
						t.Fatal(err)
					}
					pinsPath, err := config.PinsFilePath()
					if err != nil {
						t.Fatal(err)
					}
					settingsPath := filepath.Join(filepath.Dir(pinsPath), "settings.yml")
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
					paths := []string{path, pinsPath, settingsPath, shim, filepath.Join(filepath.Dir(pinsPath), "native-gh-targets.json"), helper, os.Getenv("GIT_CONFIG_GLOBAL"), identity}
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
					for range 2 {
						setup := NewSetupCmd()
						if !implicit {
							setup.SetArgs([]string{"--native"})
						}
						err = setup.Execute()
						if err == nil || !strings.Contains(err.Error(), "direnv deny") || !strings.Contains(err.Error(), path) {
							t.Fatalf("expected manual cleanup refusal: %v", err)
						}
						for i, p := range paths {
							after, err := snapshotSetupFile(p)
							if err != nil || !reflect.DeepEqual(before[i], after) {
								t.Errorf("%s changed: %v", p, err)
							}
						}
					}
					if _, err := os.Stat(log); !os.IsNotExist(err) {
						t.Fatalf("direnv invoked: %v", err)
					}
					if err := os.WriteFile(path, []byte("before\r\nafter\r\n"), 0600); err != nil {
						t.Fatal(err)
					}
					setup := NewSetupCmd()
					if !implicit {
						setup.SetArgs([]string{"--native"})
					}
					if err := setup.Execute(); err != nil {
						t.Fatal(err)
					}
					if data, err := os.ReadFile(path); err != nil || string(data) != "before\r\nafter\r\n" {
						t.Fatalf("unmanaged envrc changed: %v", err)
					}
				})
			}
		}
	}
}

func TestNativeMigrationManualCleanupRetry(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	first, second := filepath.Join(home, "first"), filepath.Join(home, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: first, User: "alice"}, {Dir: second, User: "alice"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	const unmanaged = "before\nafter\n"
	const managed = "before\n# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\nafter\n"
	firstPath, secondPath := filepath.Join(first, ".envrc"), filepath.Join(second, ".envrc")
	for path, content := range map[string]string{firstPath: unmanaged, secondPath: managed} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	pinsPath, _ := config.PinsFilePath()
	settingsPath := filepath.Join(filepath.Dir(pinsPath), "settings.yml")
	beforePins, _ := os.ReadFile(pinsPath)
	beforeSettings, _ := os.ReadFile(settingsPath)
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--native", "--migrate"})
	if err := setup.Execute(); err == nil || !strings.Contains(err.Error(), secondPath) {
		t.Fatalf("expected second pin refusal: %v", err)
	}
	for path, expected := range map[string]string{firstPath: unmanaged, secondPath: managed, pinsPath: string(beforePins), settingsPath: string(beforeSettings)} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != expected {
			t.Fatalf("%s changed: %q %v", path, data, err)
		}
	}
	if err := os.WriteFile(secondPath, []byte(unmanaged), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		setup = NewSetupCmd()
		setup.SetArgs([]string{"--native", "--migrate"})
		if err := setup.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{firstPath, secondPath} {
			data, err := os.ReadFile(path)
			if err != nil || string(data) != unmanaged {
				t.Fatalf("unmanaged bytes changed: %q %v", data, err)
			}
		}
	}
}

func TestNativeSetupWithoutMigrationExportPin(t *testing.T) {
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
	setup.SetArgs([]string{"--native"})
	if err := setup.Execute(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != content {
		t.Fatalf("export envrc changed: %v", err)
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
