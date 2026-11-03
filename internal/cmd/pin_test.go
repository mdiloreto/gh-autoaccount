package cmd

import (
	"fmt"
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
)

func TestPinUnpinRefreshHostHelpers(t *testing.T) {
	home := onboardingHome(t)
	if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
		t.Fatal(err)
	}
	if err := gitcred.ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(home, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	broker := filepath.Join(home, "broker")
	if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf '%s\\n' test-token\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "config", "--global", "--add", "credential.https://ghe.example.com.helper", "prior-enterprise").CombinedOutput(); err != nil {
		t.Fatalf("seed: %s %v", out, err)
	}
	out := captureOnboarding(t, func() error { return runPinWithHost("alice", repo, "", "", "", "GHE.EXAMPLE.COM", broker, false) })
	if !strings.Contains(out, "Resolving credential for 'alice'... OK (remote identity not verified)") || strings.Contains(out, "Validating account") {
		t.Fatalf("misleading credential validation output: %s", out)
	}
	registry, err := config.LoadPins()
	if err != nil || len(registry.Pins) != 1 || registry.Pins[0].Host != "ghe.example.com" {
		t.Fatalf("pin: %+v %v", registry, err)
	}
	if !gitcred.CheckConfiguredHost("ghe.example.com") {
		t.Fatal("enterprise helper missing")
	}
	before, err := gitcred.SnapshotHosts([]string{"ghe.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "config", "--global", "--add", "credential.https://github.com.helper", "later-helper").CombinedOutput(); err != nil {
		t.Fatalf("seed: %s %v", out, err)
	}
	captureOnboarding(t, func() error { return runUnpin(NewUnpinCmd(), []string{repo}) })
	after, err := gitcred.SnapshotHosts([]string{"ghe.example.com"})
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("removed host section changed: %#v %#v %v", before, after, err)
	}
	github, err := gitcred.HelperSummaryHost("github.com")
	if err != nil || len(github) < 2 || github[len(github)-2] != "later-helper" || github[len(github)-1] != "!gh auth git-credential" {
		t.Fatalf("github refresh: %q %v", github, err)
	}
}

func TestPinUnpinRefreshFailureRollsBackIdentity(t *testing.T) {
	for _, operation := range []string{"pin", "unpin"} {
		t.Run(operation, func(t *testing.T) {
			home := onboardingHome(t)
			if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
				t.Fatal(err)
			}
			if err := gitidentity.Install(&config.PinRegistry{}); err != nil {
				t.Fatal(err)
			}
			if err := gitcred.ConfigureGit(); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(home, "repo")
			if err := os.Mkdir(repo, 0700); err != nil {
				t.Fatal(err)
			}
			broker := filepath.Join(home, "broker")
			if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf '%s\\n' token\n"), 0700); err != nil {
				t.Fatal(err)
			}
			pin := func() error {
				return runPinWithHost("alice", repo, "alice@example.com", "Alice", "", "ghe.example.com", broker, false)
			}
			if operation == "unpin" {
				captureOnboarding(t, pin)
			}
			previous, err := config.LoadPins()
			if err != nil {
				t.Fatal(err)
			}
			identity, err := gitidentity.Path()
			if err != nil {
				t.Fatal(err)
			}
			identityBefore, err := os.ReadFile(identity)
			if err != nil {
				t.Fatal(err)
			}
			hosts := []string{"github.com", "ghe.example.com"}
			helperBefore, err := gitcred.SnapshotHosts(hosts)
			if err != nil {
				t.Fatal(err)
			}
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			wrapper := filepath.Join(home, "bin", "git")
			script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *'--add credential.https://'*) if [ ! -e \"$TEST_FAIL_MARKER\" ]; then : > \"$TEST_FAIL_MARKER\"; exit 1; fi;; esac\nexec %q \"$@\"\n", realGit)
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_FAIL_MARKER", filepath.Join(home, "failed"))
			if operation == "pin" {
				err = pin()
			} else {
				err = runUnpin(NewUnpinCmd(), []string{repo})
			}
			if err == nil || !strings.Contains(err.Error(), "cannot refresh Git helpers") {
				t.Fatalf("expected refresh failure: %v", err)
			}
			if _, err := os.Stat(filepath.Join(home, "failed")); err != nil {
				t.Fatalf("failure not injected: %v", err)
			}
			after, err := config.LoadPins()
			if err != nil || len(previous.Pins) != len(after.Pins) || (len(previous.Pins) != 0 && !reflect.DeepEqual(previous.Pins, after.Pins)) {
				t.Fatalf("registry changed: %#v %#v %v", previous, after, err)
			}
			identityAfter, err := os.ReadFile(identity)
			if err != nil || string(identityBefore) != string(identityAfter) {
				t.Fatalf("identity changed: %q %q %v", identityBefore, identityAfter, err)
			}
			helperAfter, err := gitcred.SnapshotHosts(hosts)
			if err != nil || !reflect.DeepEqual(helperBefore, helperAfter) {
				t.Fatalf("helpers changed: %#v %#v %v", helperBefore, helperAfter, err)
			}
		})
	}
}

func TestUnpinDeletedDirectoryReportsCleanupSkipped(t *testing.T) {
	home := onboardingHome(t)
	repo := filepath.Join(home, "deleted")
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{User: "alice", Dir: repo}}}); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, func() error { return runUnpin(NewUnpinCmd(), []string{repo}) })
	if !strings.Contains(out, "directory inaccessible; .envrc cleanup skipped") {
		t.Fatal(out)
	}
	registry, err := config.LoadPins()
	if err != nil || len(registry.Pins) != 0 {
		t.Fatalf("pin not removed: %+v %v", registry, err)
	}
}
