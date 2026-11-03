package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
)

func TestExecVerifyIdentity(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "autoprofile")
	build := exec.Command("go", "build", "-o", binary, "./cmd/gh-autoaccount")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	pinned := filepath.Join(root, "pinned")
	unpinned := filepath.Join(root, "unpinned")
	for _, dir := range []string{pinned, unpinned} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(pinned, alias); err != nil {
		t.Fatal(err)
	}
	broker := filepath.Join(root, "broker")
	gh := filepath.Join(root, "gh")
	child := filepath.Join(root, "child")
	for path, body := range map[string]string{
		broker: "#!/bin/sh\ntest \"$GH_AUTOPROFILE_USER:$GH_AUTOPROFILE_HOST\" = alice:enterprise.example || exit 1\nprintf 'broker-token\\n'\n",
		gh:     "#!/bin/sh\ntest \"$*\" = 'api --hostname enterprise.example user --jq .login' || exit 1\ntest \"$GH_ENTERPRISE_TOKEN:$GH_HOST\" = broker-token:enterprise.example || exit 2\ntest -z \"$GH_TOKEN$GITHUB_TOKEN$GITHUB_ENTERPRISE_TOKEN\" || exit 3\ncase \"$VERIFY_CASE\" in fail) exit 4;; mismatch) printf 'bob\\n';; *) printf 'ALICE\\n';; esac\n",
		child:  "#!/bin/sh\nprintf '%s|%s|%s|%s\\n' \"$PWD\" \"$GH_AUTOPROFILE_USER\" \"$GH_AUTOPROFILE_HOST\" \"$GH_ENTERPRISE_TOKEN\" > \"$CHILD_LOG\"\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GH_TOKEN", "stale")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "stale")
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: pinned, User: "alice", Host: "enterprise.example", TokenCommand: broker}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dir, verifyCase string
		flags                 []string
		success               bool
	}{
		{"success canonical alias and same user", alias, "ok", []string{"--require-pin", "--user", "ALICE", "--verify-identity"}, true},
		{"mismatch", pinned, "mismatch", []string{"--verify-identity"}, false},
		{"api failure", pinned, "fail", []string{"--verify-identity"}, false},
		{"unselected", unpinned, "ok", []string{"--verify-identity"}, false},
		{"conflicting override", pinned, "ok", []string{"--require-pin", "--user", "bob", "--verify-identity"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "child")
			command := exec.Command(binary, append(append([]string{"exec", "--dir", tc.dir}, tc.flags...), "--", child)...)
			command.Env = append(os.Environ(), "VERIFY_CASE="+tc.verifyCase, "CHILD_LOG="+log)
			out, err := command.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("error %v, output %q", err, out)
			}
			value, readErr := os.ReadFile(log)
			if !tc.success {
				if !os.IsNotExist(readErr) {
					t.Fatalf("child ran: %q %v", value, readErr)
				}
				return
			}
			if readErr != nil || strings.TrimSpace(string(value)) != pinned+"|alice|enterprise.example|broker-token" {
				t.Fatalf("child output %q: %v", value, readErr)
			}
		})
	}
	if err := os.WriteFile(gh, []byte("#!/bin/sh\ncase \"$1 $2\" in\n'auth token') printf 'stored-token\\n';;\n'api --hostname') test \"$3\" = github.com || exit 2; test \"$GH_TOKEN\" = stored-token || exit 3; case \"$VERIFY_CASE\" in mismatch) printf 'bob\\n';; *) printf 'ALICE\\n';; esac;;\n*) exit 4;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, dir, verifyCase string
		flags                 []string
		success               bool
	}{
		{"default", unpinned, "ok", nil, true},
		{"explicit shadows enterprise pin", pinned, "ok", []string{"--user", "alice"}, true},
		{"default mismatch", unpinned, "mismatch", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "child")
			args := append([]string{"exec", "--dir", tc.dir, "--verify-identity"}, tc.flags...)
			args = append(args, "--", child)
			command := exec.Command(binary, args...)
			command.Env = append(os.Environ(), "VERIFY_CASE="+tc.verifyCase, "CHILD_LOG="+log)
			out, err := command.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("verify: %q %v", out, err)
			}
			_, readErr := os.Stat(log)
			if tc.success && readErr != nil || !tc.success && !os.IsNotExist(readErr) {
				t.Fatalf("child execution: %v", readErr)
			}
		})
	}
}

func TestExecOverridePreservesManagedGitConfigWithoutInjectingPinIdentity(t *testing.T) {
	moduleCache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	home := onboardingHome(t)
	t.Setenv("GOMODCACHE", strings.TrimSpace(string(moduleCache)))
	t.Setenv("GOPROXY", "off")
	repo := filepath.Join(home, "repo")
	if out, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: repo, User: "bob", GitName: "Pinned Name", GitEmail: "pinned@example.test", SSHKey: "/example/key"}}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := gitidentity.Install(registry); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(home, "candidate")
	build := exec.Command("go", "build", "-o", binary, "./cmd/gh-autoaccount")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	command := exec.Command(binary, "exec", "--dir", repo, "--user", "alice", "--", "/bin/sh", "-c", `printf '%s|%s|%s|%s|%s\n' "$GH_AUTOPROFILE_USER" "${GIT_AUTHOR_EMAIL-}" "${GIT_AUTHOR_NAME-}" "${GIT_SSH_COMMAND-}" "$(git config --get user.email)"`)
	out, err := command.CombinedOutput()
	if err != nil || string(out) != "alice||||pinned@example.test\n" {
		t.Fatalf("override: %q %v", out, err)
	}
}
