package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
)

func TestAuthIntegration(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "gh-autoprofile")
	build := exec.Command("go", "build", "-o", binary, "./cmd/gh-autoaccount")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config space's"))
	t.Setenv("GH_CONFIG_DIR", filepath.Join(dir, "gh-config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", "/bin/false")
	t.Setenv("GH_AUTOPROFILE_DISABLE_SHIM", "")
	t.Setenv("GH_AUTOPROFILE_USER", "old-account")
	t.Setenv("GH_AUTOPROFILE_MODE", "export")
	t.Setenv("GH_TOKEN", "old-token")
	t.Setenv("GITHUB_TOKEN", "old-github-token")
	t.Setenv("TEST_TOKEN_CASE", "good")
	gh := filepath.Join(dir, "gh")
	script := `#!/bin/sh
if [ "$1 $2" = 'auth token' ]; then
  test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}" || exit 91
  test "$5 $6" = '--hostname github.com' || exit 92
  case "$TEST_TOKEN_CASE" in
    failure) printf 'secret-partial\n'; exit 1 ;;
    empty) exit 0 ;;
    injection) printf 'secret\npassword=injected\n'; exit 0 ;;
    good) printf 'stored-%s\n' "$4"; exit 0 ;;
  esac
fi
printf '%s|%s|%s|%s\n' "$GH_AUTOPROFILE_USER" "$GH_AUTOPROFILE_MODE" "$GH_TOKEN" "$GITHUB_TOKEN"
`
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	personal, work, unpinned := filepath.Join(dir, "personal"), filepath.Join(dir, "work"), filepath.Join(dir, "unpinned")
	for _, root := range []string{personal, work, unpinned} {
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	registry := &config.PinRegistry{Pins: []config.Pin{
		{User: "alice", Dir: personal},
		{User: "bob", Dir: work, Mode: config.ModeWrapper},
	}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	t.Run("default and enterprise helper host isolation", func(t *testing.T) {
		if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := config.SaveSettings(config.Settings{}); err != nil {
				t.Error(err)
			}
		})
		broker := filepath.Join(dir, "default-test-broker")
		if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf 'enterprise-token\\n'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		registry.Pins[1] = config.Pin{User: "bob", Dir: work, Host: "enterprise.example.invalid", TokenCommand: broker, Mode: config.ModeExport}
		if err := config.SavePins(registry); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			registry.Pins[1] = config.Pin{User: "bob", Dir: work, Mode: config.ModeWrapper}
			if err := config.SavePins(registry); err != nil {
				t.Error(err)
			}
		})
		t.Setenv("GH_AUTOPROFILE_USER", "bob")
		t.Setenv("GH_AUTOPROFILE_HOST", "enterprise.example.invalid")
		t.Setenv("GH_ENTERPRISE_TOKEN", "ambient-enterprise")
		for _, helper := range []string{"__git-credential", "credential-helper"} {
			for _, tc := range []struct{ cwd, host, password string }{
				{unpinned, "github.com", "stored-alice"},
				{unpinned, "enterprise.example.invalid", ""},
				{work, "enterprise.example.invalid", "enterprise-token"},
				{work, "github.com", ""},
			} {
				cmd := exec.Command(binary, helper, "get")
				cmd.Dir = tc.cwd
				cmd.Stdin = strings.NewReader("protocol=https\nhost=" + tc.host + "\n\n")
				out, err := cmd.Output()
				if tc.password != "" {
					if err != nil || !strings.Contains(string(out), "password="+tc.password+"\n") {
						t.Fatalf("%s %s: %q %v", helper, tc.host, out, err)
					}
				} else if err == nil || string(out) != "quit=true\n\n" {
					t.Fatalf("%s wrong host %s: %q %v", helper, tc.host, out, err)
				}
			}
		}
		if err := os.WriteFile(broker, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		for _, helper := range []string{"__git-credential", "credential-helper"} {
			cmd := exec.Command(binary, helper, "get")
			cmd.Dir = work
			cmd.Stdin = strings.NewReader("protocol=https\nhost=enterprise.example.invalid\n\n")
			out, err := cmd.Output()
			if err == nil || string(out) != "quit=true\n\n" {
				t.Fatalf("%s broker failure: %q %v", helper, out, err)
			}
		}
		cmd := exec.Command(binary, "exec", "--", "gh", "api", "user")
		cmd.Dir = work
		out, err := cmd.Output()
		if err == nil || strings.Contains(string(out), "ambient-enterprise") {
			t.Fatalf("broker ambient fallback: %q %v", out, err)
		}
	})

	t.Run("host-scoped real git credential fill", func(t *testing.T) {
		broker := filepath.Join(dir, "broker")
		if err := os.WriteFile(broker, []byte("#!/bin/sh\ntest \"$GH_AUTOPROFILE_HOST\" = ghe.example.com || exit 1\ntest \"$GH_AUTOPROFILE_USER\" = alice || exit 1\nprintf '%s\\n' enterprise-token\n"), 0700); err != nil {
			t.Fatal(err)
		}
		registry.Pins[1] = config.Pin{User: "alice", Dir: work, Host: "ghe.example.com", TokenCommand: broker}
		if err := config.SavePins(registry); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			registry.Pins[1] = config.Pin{User: "bob", Dir: work, Mode: config.ModeWrapper}
			if err := config.SavePins(registry); err != nil {
				t.Error(err)
			}
		})
		if _, err := gitcred.Install(binary, gh); err != nil {
			t.Fatal(err)
		}
		if err := gitcred.ConfigureHosts([]string{"github.com", "ghe.example.com"}); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ cwd, host, token string }{{personal, "github.com", "stored-alice"}, {work, "ghe.example.com", "enterprise-token"}} {
			cmd := exec.Command("git", "credential", "fill")
			cmd.Dir = tc.cwd
			cmd.Stdin = strings.NewReader("protocol=https\nhost=" + tc.host + "\n\n")
			out, err := cmd.Output()
			if err != nil || !strings.Contains(string(out), "password="+tc.token+"\n") {
				t.Fatalf("%s: %q: %v", tc.host, out, err)
			}
		}
		if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf secret-fragment\nprintf secret-fragment >&2\nexit 1\n"), 0700); err != nil {
			t.Fatal(err)
		}
		request := "protocol=https\nhost=ghe.example.com\n\n"
		for _, args := range [][]string{{"__git-credential", "get"}, {"credential-helper", "get"}} {
			cmd := exec.Command(binary, args...)
			cmd.Dir, cmd.Stdin = work, strings.NewReader(request)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err == nil || string(out) != "quit=true\n\n" || strings.Contains(stderr.String(), "secret-fragment") {
				t.Fatalf("helper %v: stdout %q stderr %q error %v", args, out, stderr.String(), err)
			}
		}
		fallbackLog := filepath.Join(dir, "enterprise-fallback")
		t.Setenv("TEST_ENTERPRISE_FALLBACK", fallbackLog)
		fallback := `!f() { printf called > "$TEST_ENTERPRISE_FALLBACK"; printf 'username=fallback\npassword=fallback\n'; }; f`
		cmd := exec.Command("git", "-c", "credential.https://ghe.example.com.helper="+fallback, "credential", "fill")
		cmd.Dir, cmd.Stdin = work, strings.NewReader(request)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err == nil || len(out) != 0 || strings.Contains(stderr.String(), "secret-fragment") || !strings.Contains(stderr.String(), "quit") {
			t.Fatalf("git fill stdout %q stderr %q error %v", out, stderr.String(), err)
		}
		if _, err := os.Stat(fallbackLog); !os.IsNotExist(err) {
			t.Fatalf("fallback executed: %v", err)
		}
	})

	t.Run("installed helper ignores repository gh and inherited override", func(t *testing.T) {
		attackerDir := filepath.Join(personal, "attacker-bin")
		if err := os.Mkdir(attackerDir, 0700); err != nil {
			t.Fatal(err)
		}
		attacker := filepath.Join(attackerDir, "gh")
		log := filepath.Join(dir, "attacker-log")
		if err := os.WriteFile(attacker, []byte("#!/bin/sh\nprintenv > \"$ATTACKER_LOG\"\nprintf attacker-token\\n\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := gitcred.Install(binary, gh); err != nil {
			t.Fatal(err)
		}
		if err := gitcred.ConfigureGit(); err != nil {
			t.Fatal(err)
		}
		t.Setenv("ATTACKER_LOG", log)
		t.Setenv("PATH", attackerDir+string(os.PathListSeparator)+os.Getenv("PATH"))
		t.Setenv("GH_AUTOPROFILE_REAL_GH", attacker)
		t.Setenv("GH_PATH", attacker)
		cmd := exec.Command("git", "credential", "fill")
		cmd.Dir, cmd.Stdin = personal, strings.NewReader("protocol=https\nhost=github.com\n\n")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "password=stored-alice\n") || strings.Contains(string(out), "attacker-token") {
			t.Fatalf("git fill: %q %v", out, err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Fatalf("attacker gh ran or received credential: %v", err)
		}
	})

	t.Run("directory and explicit user override stale auth", func(t *testing.T) {
		for _, tc := range []struct {
			name, cwd, want string
			args            []string
		}{
			{"exec personal", work, "alice|wrapper|stored-alice|stored-alice\n", []string{"exec", "--dir", personal, "--", "gh", "api", "user"}},
			{"exec work", personal, "bob|wrapper|stored-bob|stored-bob\n", []string{"exec", "--dir", work, "--", "gh", "api", "user"}},
			{"exec explicit user", work, "alice|wrapper|stored-alice|stored-alice\n", []string{"exec", "--user", "alice", "--", "gh", "api", "user"}},
			{"exec unpinned", work, "|||\n", []string{"exec", "--dir", unpinned, "--", "gh", "api", "user"}},
			{"shim personal", personal, "alice|wrapper|stored-alice|\n", []string{"__shim-gh", "api", "user"}},
			{"shim work", work, "bob|wrapper|stored-bob|\n", []string{"__shim-gh", "api", "user"}},
			{"shim unpinned", unpinned, "|||\n", []string{"__shim-gh", "api", "user"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cmd := exec.Command(binary, tc.args...)
				cmd.Dir = tc.cwd
				out, err := cmd.Output()
				if err != nil || string(out) != tc.want {
					t.Fatalf("output %q, error %v; want %q", out, err, tc.want)
				}
			})
		}
	})

	t.Run("export-mode pin never takes the ambient token", func(t *testing.T) {
		registry.Pins[1].Mode = config.ModeExport
		if err := config.SavePins(registry); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			registry.Pins[1].Mode = config.ModeWrapper
			if err := config.SavePins(registry); err != nil {
				t.Error(err)
			}
		})
		for _, marker := range []string{"", "bob", "alice"} {
			t.Setenv("GH_AUTOPROFILE_USER", marker)
			for _, entrypoint := range []string{"exec", "__shim-gh"} {
				args := []string{entrypoint, "api", "user"}
				if entrypoint == "exec" {
					args = []string{"exec", "--", "gh", "api", "user"}
				}
				cmd := exec.Command(binary, args...)
				cmd.Dir = work
				out, err := cmd.Output()
				want := "bob|export|stored-bob|stored-bob\n"
				if entrypoint == "__shim-gh" {
					want = "bob|export|stored-bob|\n"
				}
				if err != nil || string(out) != want {
					t.Fatalf("%s marker %q: output %q, error %v; want %q", entrypoint, marker, out, err, want)
				}
			}
		}
		for _, tc := range []struct {
			user, host, want string
		}{
			{"", "", "bob|export|stored-bob|\n"},
			{"bob", "github.com", "bob|export|stored-bob|\n"},
			{"bob", "", "bob|export|stored-bob|\n"},
			{"alice", "github.com", "bob|export|stored-bob|\n"},
			{"bob", "enterprise.example", "bob|export|stored-bob|\n"},
		} {
			t.Setenv("GH_AUTOPROFILE_USER", tc.user)
			t.Setenv("GH_AUTOPROFILE_HOST", tc.host)
			cmd := exec.Command(binary, "__shim-gh", "api", "user")
			cmd.Dir = work
			out, err := cmd.Output()
			if err != nil || string(out) != tc.want {
				t.Fatalf("matching markers %q/%q: output %q, error %v; want %q", tc.user, tc.host, out, err, tc.want)
			}
		}
		t.Setenv("GH_AUTOPROFILE_USER", "")
		t.Setenv("GH_AUTOPROFILE_HOST", "")
		cmd := exec.Command(binary, "__shim-gh", "api", "user")
		cmd.Dir = unpinned
		if out, err := cmd.Output(); err != nil || string(out) != "|||\n" {
			t.Fatalf("unpinned explicit env: %q, %v", out, err)
		}
	})

	t.Run("shim and exec never run on token failure", func(t *testing.T) {
		for _, failure := range []string{"empty", "failure", "injection"} {
			t.Setenv("TEST_TOKEN_CASE", failure)
			for _, args := range [][]string{{"__shim-gh", "api", "user"}, {"exec", "--", "gh", "api", "user"}} {
				cmd := exec.Command(binary, args...)
				cmd.Dir = work
				if out, err := cmd.Output(); err == nil || len(out) != 0 {
					t.Fatalf("%v with %s: output %q, error %v", args, failure, out, err)
				}
			}
		}
		for _, action := range []string{"switch", "login", "logout", "refresh", "setup-git"} {
			cmd := exec.Command(binary, "__shim-gh", "auth", action)
			cmd.Dir = work
			if out, err := cmd.Output(); err != nil || len(out) == 0 {
				t.Fatalf("auth %s bypass: %q, %v", action, out, err)
			}
		}
	})

	t.Run("exec override inherited by Git and nested scripts", func(t *testing.T) {
		if _, err := gitcred.Install(binary, gh); err != nil {
			t.Fatal(err)
		}
		if err := gitcred.ConfigureGit(); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "exec", "--user", "alice", "--", "git", "credential", "fill")
		cmd.Dir = work
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
		if out, err := cmd.Output(); err != nil || !strings.Contains(string(out), "password=stored-alice\n") {
			t.Fatalf("explicit account lost to Bob's installed helper: %q, %v", out, err)
		}
		// This existing helper wins unless exec appends a reset for GitHub HTTPS.
		t.Setenv("GIT_CONFIG_COUNT", "2")
		t.Setenv("GIT_CONFIG_KEY_0", "test.preserved")
		t.Setenv("GIT_CONFIG_VALUE_0", "keep-me")
		t.Setenv("GIT_CONFIG_KEY_1", "credential.helper")
		t.Setenv("GIT_CONFIG_VALUE_1", `!f() { printf 'username=other\npassword=other-host-token\n'; }; f`)
		for _, nested := range []bool{false, true} {
			for _, request := range []struct {
				protocol, host, want string
			}{
				{"https", "github.com", "stored-alice"},
				{"http", "github.com", "other-host-token"},
				{"https", "example.com", "other-host-token"},
				{"https", "github.com.evil.example", "other-host-token"},
				{"https", "api.github.com", "other-host-token"},
				{"https", "github.com:8443", "other-host-token"},
				{"ssh", "github.com", "other-host-token"},
			} {
				args := []string{"exec", "--user", "alice", "--", "git", "credential", "fill"}
				if nested {
					args = []string{"exec", "--user", "alice", "--", "sh", "-c", `test "$(git config test.preserved)" = keep-me && sh -c 'git credential fill'`}
				}
				cmd := exec.Command(binary, args...)
				cmd.Dir = work
				cmd.Stdin = strings.NewReader("protocol=" + request.protocol + "\nhost=" + request.host + "\n\n")
				out, err := cmd.Output()
				if err != nil || !strings.Contains(string(out), "password="+request.want+"\n") {
					t.Fatalf("nested=%v %s://%s: output %q, error %v", nested, request.protocol, request.host, out, err)
				}
				if request.want != "stored-alice" && strings.Contains(string(out), "stored-alice") {
					t.Fatalf("selected token leaked: %q", out)
				}
			}
		}
		for _, action := range []string{"approve", "reject"} {
			cmd := exec.Command(binary, "exec", "--user", "alice", "--", "git", "credential", action)
			cmd.Dir = work
			cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\nusername=test\npassword=test\n\n")
			if out, err := cmd.Output(); err != nil || len(out) != 0 {
				t.Fatalf("exec git %s: output %q, error %v", action, out, err)
			}
		}
		for _, count := range []string{"invalid", "-1", "9223372036854775807"} {
			t.Setenv("GIT_CONFIG_COUNT", count)
			cmd := exec.Command(binary, "exec", "--user", "alice", "--", "gh", "api", "user")
			cmd.Dir = work
			if out, err := cmd.Output(); err == nil || len(out) != 0 {
				t.Fatalf("invalid count %s ran child: %q, %v", count, out, err)
			}
		}
	})

	t.Run("nested exec unpinned restores caller Git config", func(t *testing.T) {
		t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
		t.Setenv("GIT_CONFIG_COUNT", "2")
		t.Setenv("GIT_CONFIG_KEY_0", "test.original")
		t.Setenv("GIT_CONFIG_VALUE_0", "original-value")
		t.Setenv("GIT_CONFIG_KEY_1", "credential.helper")
		t.Setenv("GIT_CONFIG_VALUE_1", `!f() { printf 'username=fallback\npassword=fallback-token\n'; }; f`)
		t.Setenv("TEST_AUTOPROFILE", binary)
		t.Setenv("TEST_UNPINNED", unpinned)
		for _, nested := range []bool{false, true} {
			script := `exec "$TEST_AUTOPROFILE" exec --dir "$TEST_UNPINNED" -- git credential fill`
			if nested {
				script = `
export GIT_CONFIG_KEY_5=test.later GIT_CONFIG_VALUE_5=later-value GIT_CONFIG_COUNT=6
exec "$TEST_AUTOPROFILE" exec --user bob -- sh -c '
exec "$TEST_AUTOPROFILE" exec --dir "$TEST_UNPINNED" -- sh -c '\''
test "$GIT_CONFIG_COUNT" = 3 || exit 91
test -z "$GH_AUTOPROFILE_GIT_CONFIG_START$GH_TOKEN$GITHUB_TOKEN" || exit 92
test "$(git config test.original)" = original-value || exit 93
test "$(git config test.later)" = later-value || exit 94
git credential fill
'\''
'`
			}
			cmd := exec.Command(binary, "exec", "--user", "alice", "--", "sh", "-c", script)
			cmd.Dir = work
			cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
			out, err := cmd.Output()
			want := "protocol=https\nhost=github.com\nusername=fallback\npassword=fallback-token\n"
			if err != nil || string(out) != want {
				t.Fatalf("nested=%v: output %q, error %v", nested, out, err)
			}
		}
	})

	t.Run("real git helper chain", func(t *testing.T) {
		for _, entrypoint := range []string{"__git-credential", "credential-helper"} {
			for _, action := range []string{"store", "erase"} {
				cmd := exec.Command(binary, entrypoint, action)
				cmd.Dir = work
				cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
				if out, err := cmd.Output(); err != nil || len(out) != 0 {
					t.Fatalf("%s %s emitted credentials: %q, %v", entrypoint, action, out, err)
				}
			}
		}
		if _, err := gitcred.Install(binary, gh); err != nil {
			t.Fatal(err)
		}
		if err := gitcred.ConfigureGit(); err != nil {
			t.Fatal(err)
		}
		helper, err := gitcred.HelperPath()
		if err != nil {
			t.Fatal(err)
		}
		fallbackLog := filepath.Join(dir, "fallback")
		t.Setenv("TEST_FALLBACK_LOG", fallbackLog)
		fallback := `!f() { printf called >> "$TEST_FALLBACK_LOG"; printf 'username=fallback\npassword=fallback-token\n'; }; f`
		for _, primary := range []string{"!'" + strings.ReplaceAll(helper, "'", "'\\''") + "'", "!" + binary + " credential-helper"} {
			for _, scenario := range []string{"good", "empty", "failure", "injection", "export-injection", "unpinned", "other-host", "invalid-registry"} {
				t.Run(primary+"/"+scenario, func(t *testing.T) {
					t.Setenv("TEST_TOKEN_CASE", scenario)
					cwd, host := work, "github.com"
					if scenario == "unpinned" {
						cwd = unpinned
					}
					if scenario == "other-host" {
						host = "example.com"
					}
					if scenario == "export-injection" {
						registry.Pins[1].Mode = config.ModeExport
						if err := config.SavePins(registry); err != nil {
							t.Fatal(err)
						}
						t.Setenv("GH_AUTOPROFILE_USER", "bob")
						t.Setenv("GH_TOKEN", "secret\npassword=injected")
						t.Setenv("TEST_TOKEN_CASE", "injection")
						t.Cleanup(func() {
							registry.Pins[1].Mode = config.ModeWrapper
							if err := config.SavePins(registry); err != nil {
								t.Error(err)
							}
						})
					}
					if scenario == "invalid-registry" {
						pins, _ := config.PinsFilePath()
						if err := os.WriteFile(pins, []byte("pins: ["), 0600); err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() {
							if err := config.SavePins(registry); err != nil {
								t.Error(err)
							}
						})
					}
					t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
					cmd := exec.Command("git", "-c", "credential.helper=", "-c", "credential.helper="+primary, "-c", "credential.helper="+fallback, "credential", "fill")
					cmd.Dir = cwd
					cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
					out, err := cmd.Output()
					wantFallback := scenario == "unpinned"
					wantFailure := scenario != "good" && !wantFallback
					if (err != nil) != wantFailure {
						t.Fatalf("output %q, error %v; want failure %v", out, err, wantFailure)
					}
					if wantFailure && len(out) != 0 {
						t.Fatalf("credential data leaked on failure: %q", out)
					}
					if scenario == "good" && !strings.Contains(string(out), "password=stored-bob\n") {
						t.Fatalf("wrong credential: %q", out)
					}
					_, statErr := os.Stat(fallbackLog)
					if (statErr == nil) != wantFallback {
						t.Fatalf("fallback ran = %v, want %v", statErr == nil, wantFallback)
					}
					if statErr == nil {
						if err := os.Remove(fallbackLog); err != nil {
							t.Fatal(err)
						}
					}
				})
			}
		}
	})
}
