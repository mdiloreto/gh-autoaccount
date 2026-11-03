package profileexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestAmbientSelectionMarkers(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN", "1")
	t.Setenv("GH_ENTERPRISE_TOKEN", "ambient-token")
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf 'stored-token\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: root, User: "alice", Host: "enterprise.example", Mode: config.ModeExport}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, user, host, expect, token string }{
		{"first use", "", "", "", "stored-token"},
		{"matching pair", "ALICE", "ENTERPRISE.EXAMPLE", "", "ambient-token"},
		{"user only", "alice", "", "", "stored-token"},
		{"host only", "", "enterprise.example", "", "stored-token"},
		{"same user other host", "alice", "other.example", "", "stored-token"},
		{"expect user permits ambient", "alice", "enterprise.example", "ALICE", "ambient-token"},
		{"expect user no markers", "", "", "alice", "stored-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_AUTOPROFILE_USER", tc.user)
			t.Setenv("GH_AUTOPROFILE_HOST", tc.host)
			ctx, err := ResolveContextWithPolicy(root, Policy{ExpectUser: tc.expect})
			if err != nil {
				t.Fatal(err)
			}
			if ctx.Token != tc.token {
				t.Fatalf("token %q, want %q", ctx.Token, tc.token)
			}
		})
	}
}

func TestDefaultAndExplicitSelectionDoNotFabricatePin(t *testing.T) {
	root := t.TempDir()
	pinned := filepath.Join(root, "pinned")
	if err := os.Mkdir(pinned, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf 'stored-token\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: pinned, User: "bob", Host: "enterprise.example.invalid", TokenCommand: "exit 1", GitEmail: "bob@example.invalid", GitName: "Bob", SSHKey: "/bob-key"}}}); err != nil {
		t.Fatal(err)
	}
	ctx, err := ResolveContext(root, false)
	if err != nil || ctx.Pin != nil || ctx.SelectedUser != "alice" || ctx.SelectionSource != "default" || ctx.SelectedHost != "github.com" {
		t.Fatalf("default selection: %+v %v", ctx, err)
	}
	if _, err := ResolveContext(root, true); err == nil {
		t.Fatal("default satisfied require-pin")
	}
	if _, err := ResolveContext(pinned, false); err == nil {
		t.Fatal("broker failure fell back")
	}
	ctx, err = ResolveContextWithPolicy(pinned, Policy{User: "alice"})
	if err != nil || ctx.Pin != nil || ctx.SelectedHost != "github.com" || ctx.SelectedUser != "alice" || ctx.SelectionSource != "explicit" {
		t.Fatalf("explicit selection: %+v %v", ctx, err)
	}
	t.Setenv("GIT_AUTHOR_EMAIL", "caller@example.invalid")
	t.Setenv("GIT_AUTHOR_NAME", "Caller")
	t.Setenv("GIT_SSH_COMMAND", "caller-ssh")
	t.Setenv("GH_ENTERPRISE_TOKEN", "ambient-enterprise")
	env, err := BuildExecEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(env, "GIT_AUTHOR_EMAIL") != "caller@example.invalid" || envValue(env, "GIT_AUTHOR_NAME") != "Caller" || envValue(env, "GIT_SSH_COMMAND") != "caller-ssh" || envValue(env, "GH_AUTOPROFILE_HOST") != "github.com" || envValue(env, "GH_ENTERPRISE_TOKEN") != "" || envValue(env, "GH_TOKEN") != "stored-token" {
		t.Fatalf("explicit inherited pin state: %v", env)
	}
	if _, err := ResolveContextWithPolicy(pinned, Policy{User: "alice", RequirePin: true}); err == nil {
		t.Fatal("mismatched user satisfied require-pin")
	}
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, err = ResolveContext(root, false)
	if err == nil || ctx == nil || ctx.SelectedUser != "alice" || ctx.SelectionSource != "default" || ctx.Pin != nil {
		t.Fatalf("missing default credential: %+v %v", ctx, err)
	}
	if err := config.SaveSettings(config.Settings{}); err != nil {
		t.Fatal(err)
	}
	ctx, err = ResolveContext(root, false)
	if err != nil || ctx == nil || ctx.SelectionSource != "none" || ctx.SelectedUser != "" || ctx.Pin != nil {
		t.Fatalf("no selection: %+v %v", ctx, err)
	}
}

func TestManagedHostBlockReplacementAndValidation(t *testing.T) {
	base := []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=Caller"}
	for _, host := range []string{"github.com", "tenant.ghe.com", "enterprise.example"} {
		t.Run(host, func(t *testing.T) {
			env, err := WithHostCredentials(append([]string(nil), base...), host)
			if err != nil {
				t.Fatal(err)
			}
			env, err = appendGitConfigPairs(env, [][2]string{{"user.email", "caller@example.test"}})
			if err != nil {
				t.Fatal(err)
			}
			updated, err := WithHostCredentials(append([]string(nil), env...), "other.example")
			if err != nil {
				t.Fatal(err)
			}
			if envValue(updated, "GIT_CONFIG_KEY_1") != "user.email" || envValue(updated, "GIT_CONFIG_VALUE_1") != "caller@example.test" || envValue(updated, gitHubConfigHost) != "other.example" || envValue(updated, "GIT_CONFIG_COUNT") != "5" {
				t.Fatalf("replacement lost caller config: %v", updated)
			}
			command := exec.Command("git", "credential", "fill")
			command.Env = append(updated, "PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GH_ENTERPRISE_TOKEN=replacement-token")
			command.Stdin = strings.NewReader("protocol=https\nhost=other.example\n\n")
			out, err := command.Output()
			if err != nil || !strings.Contains(string(out), "password=replacement-token\n") {
				t.Fatalf("replacement helper failed: %q %v", out, err)
			}
			for _, entry := range []string{gitHubConfigStart + "=1", gitHubConfigHost + "=" + host, "GIT_CONFIG_KEY_1=credential.https://evil.example.helper", "GIT_CONFIG_VALUE_2=modified", "GIT_CONFIG_COUNT=4"} {
				if _, err := WithoutGitHubCredentials(append(append([]string(nil), env...), entry)); err == nil {
					t.Errorf("duplicate %q accepted", entry)
				}
			}
			tampered := setEnv(append([]string(nil), env...), "GIT_CONFIG_VALUE_2", "modified")
			if _, err := WithoutGitHubCredentials(tampered); err == nil {
				t.Fatal("tampered helper accepted")
			}
			duplicate, err := appendGitConfigPairs(append([]string(nil), env...), hostCredentialPairs(host))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := WithoutGitHubCredentials(duplicate); err == nil {
				t.Fatal("unmarked duplicate managed block accepted")
			}
			if host == "github.com" {
				legacy := removeEnv(append([]string(nil), env...), gitHubConfigHost)
				clean, err := WithoutGitHubCredentials(legacy)
				if err != nil || envValue(clean, "GIT_CONFIG_KEY_1") != "user.email" {
					t.Fatalf("legacy removal: %v", err)
				}
			}
		})
	}
}

func TestHostGitCredentialFill(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", "/bin/false")
	for _, host := range []string{"github.com", "tenant.ghe.com", "enterprise.example"} {
		for _, forGit := range []bool{false, true} {
			t.Run(host+map[bool]string{false: "/exec", true: "/git"}[forGit], func(t *testing.T) {
				ctx := &Context{Pin: &config.Pin{User: "alice", Host: host}, Token: "selected-token"}
				var env []string
				var err error
				if forGit {
					env, err = BuildEnv(ctx, true)
				} else {
					env, err = BuildExecEnv(ctx)
				}
				if err != nil {
					t.Fatal(err)
				}
				fill := func(target string, inputEnv []string) (string, error) {
					cmd := exec.Command("git", "credential", "fill")
					cmd.Env = inputEnv
					cmd.Stdin = strings.NewReader("protocol=https\nhost=" + target + "\n\n")
					out, err := cmd.Output()
					return string(out), err
				}
				out, err := fill(host, env)
				if err != nil || !strings.Contains(out, "password=selected-token\n") {
					t.Fatalf("matching host: %q %v", out, err)
				}
				out, err = fill("other.example", env)
				if err == nil || strings.Contains(out, "selected-token") {
					t.Fatalf("mismatched host: %q %v", out, err)
				}
				primary := "GH_ENTERPRISE_TOKEN"
				if host == "github.com" || strings.HasSuffix(host, ".ghe.com") {
					primary = "GH_TOKEN"
				}
				out, err = fill(host, removeEnv(append([]string(nil), env...), primary))
				if err == nil || strings.Contains(out, "selected-token") {
					t.Fatalf("missing token: %q %v", out, err)
				}
			})
		}
	}
}
