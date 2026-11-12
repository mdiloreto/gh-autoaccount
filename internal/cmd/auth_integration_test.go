package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	t.Run("relative roots are not forwarded to gh", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("probe fixture requires a POSIX shell")
		}
		probe := filepath.Join(t.TempDir(), "probe-gh")
		script := `#!/bin/sh
if [ "$1 $2" = 'auth token' ]; then
  printf 'probe-token\n'
  exit 0
fi
test "$1 $2" = 'auth status' && test "$GH_TOKEN" = 'probe-token' || exit 1
printf 'config=%s\ndata=%s\nghconfig=%s\n' "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$GH_CONFIG_DIR"
`
		if err := os.WriteFile(probe, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
		if err := config.SavePins(registry); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GH_PATH", "")
		t.Setenv("GH_AUTOPROFILE_REAL_GH", probe)
		t.Setenv("GH_AUTOACCOUNT_USER", "")
		configHome := filepath.Join(dir, "config space's")
		dataHome := filepath.Join(dir, "data")
		ghConfig := filepath.Join(dir, "gh-config")
		for _, tc := range []struct {
			name, configHome, dataHome, ghConfig, want string
		}{
			{name: "relative", configHome: ".", dataHome: ".", ghConfig: "relative-gh", want: "config=\ndata=\nghconfig=\n"},
			{name: "absolute", configHome: configHome, dataHome: dataHome, ghConfig: ghConfig, want: fmt.Sprintf("config=%s\ndata=%s\nghconfig=%s\n", configHome, dataHome, ghConfig)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", tc.configHome)
				t.Setenv("XDG_DATA_HOME", tc.dataHome)
				t.Setenv("GH_CONFIG_DIR", tc.ghConfig)
				command := exec.Command(binary, "__shim-gh", "auth", "status")
				command.Dir = personal
				out, err := command.CombinedOutput()
				if err != nil || string(out) != tc.want {
					t.Fatalf("gh child = %q, %v; want %q", out, err, tc.want)
				}
			})
		}
	})
	t.Run("relative XDG cannot redirect child Git global config", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("credential fixture requires a POSIX shell")
		}
		if _, err := exec.LookPath("git"); err != nil {
			t.Skip("git unavailable")
		}
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
		if err := config.SavePins(registry); err != nil {
			t.Fatal(err)
		}
		for path, name := range map[string]string{
			filepath.Join(dir, ".config", "git", "config"): "trusted",
			filepath.Join(personal, "git", "config"):       "repository",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("[user]\n\tname = "+name+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("XDG_CONFIG_HOME", ".")
		t.Setenv("GH_PATH", "")
		t.Setenv("GH_AUTOACCOUNT_USER", "")
		command := exec.Command(binary, "exec", "--", "git", "config", "--global", "user.name")
		command.Dir = personal
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GIT_CONFIG_GLOBAL=") {
				command.Env = append(command.Env, entry)
			}
		}
		out, err := command.CombinedOutput()
		if err != nil || string(out) != "trusted\n" {
			t.Fatalf("child Git global name = %q, %v; want trusted", out, err)
		}
	})
	t.Run("real gh ignores relative config and data roots", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("real gh fixture requires Linux")
		}
		const realGH = "/usr/bin/gh"
		if _, err := os.Stat(realGH); err != nil {
			t.Skipf("real gh unavailable: %v", err)
		}
		root := t.TempDir()
		home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
		for path, token := range map[string]string{
			filepath.Join(home, ".config", "gh", "hosts.yml"): "trusted-token",
			filepath.Join(repo, "gh", "hosts.yml"):            "repository-token",
			filepath.Join(repo, "relative-gh", "hosts.yml"):   "repository-token",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			contents := fmt.Sprintf("github.com:\n  user: fixture\n  oauth_token: %s\n  users:\n    fixture:\n      oauth_token: %s\n", token, token)
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		for path, output := range map[string]string{
			filepath.Join(home, ".local", "share", "gh", "extensions", "gh-evil", "gh-evil"): "trusted-extension",
			filepath.Join(repo, "gh", "extensions", "gh-evil", "gh-evil"):                    "repository-extension",
		} {
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf '"+output+"\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		for name, value := range map[string]string{
			"HOME":                            home,
			"USERPROFILE":                     home,
			"XDG_CONFIG_HOME":                 ".",
			"XDG_DATA_HOME":                   ".",
			"GH_PATH":                         realGH,
			"GH_AUTOPROFILE_REAL_GH":          realGH,
			"GH_AUTOPROFILE_DISABLE_SHIM":     "1",
			"GH_HOST":                         "github.com",
			"GH_REPO":                         "",
			"GH_TOKEN":                        "",
			"GITHUB_TOKEN":                    "",
			"GH_PROMPT_DISABLED":              "1",
			"GH_NO_UPDATE_NOTIFIER":           "1",
			"GH_NO_EXTENSION_UPDATE_NOTIFIER": "1",
			"GH_TELEMETRY":                    "0",
		} {
			t.Setenv(name, value)
		}
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
			t.Setenv(name, "http://127.0.0.1:1")
		}
		t.Setenv("NO_PROXY", "")
		for _, ghConfig := range []string{"", "relative-gh"} {
			t.Run("token with GH_CONFIG_DIR="+ghConfig, func(t *testing.T) {
				t.Setenv("GH_CONFIG_DIR", ghConfig)
				command := exec.Command(binary, "__shim-gh", "auth", "token")
				command.Dir = repo
				out, err := command.CombinedOutput()
				if err != nil || string(out) != "trusted-token\n" {
					t.Fatalf("gh auth token = %q, %v; want trusted token", out, err)
				}
			})
		}
		t.Setenv("GH_CONFIG_DIR", "")
		command := exec.Command(binary, "__shim-gh", "evil")
		command.Dir = repo
		out, err := command.CombinedOutput()
		if err != nil || string(out) != "trusted-extension\n" {
			t.Fatalf("gh extension = %q, %v; want trusted extension", out, err)
		}
	})
	t.Run("doctor error strips terminal controls", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows does not allow control characters in paths")
		}
		root := filepath.Join(t.TempDir(), "config-\x1b[31m")
		if err := os.MkdirAll(filepath.Join(root, "gh-autoprofile", "settings.yml"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("XDG_CONFIG_HOME", root)
		out, err := exec.Command(binary, "doctor").CombinedOutput()
		if err == nil || !strings.Contains(string(out), "cannot load setup settings") || !strings.Contains(string(out), "config-[31m") {
			t.Fatalf("doctor = %q, %v; want sanitized path error", out, err)
		}
		for _, r := range string(out) {
			if r < 0x20 && r != '\n' || r == 0x7f || r >= 0x80 && r <= 0x9f {
				t.Fatalf("doctor error emitted terminal control U+%04X: %q", r, out)
			}
		}
	})
	t.Run("direct proxy error strips terminal controls", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fixture requires a symlink and control characters in paths")
		}
		root := t.TempDir()
		proxy := filepath.Join(root, "gh")
		if err := os.Symlink(binary, proxy); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GH_PATH", filepath.Join(root, "missing-\x1b[31m"))
		command := exec.Command(proxy, "auth", "status")
		command.Dir = personal
		out, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "missing-[31m") {
			t.Fatalf("proxy = %q, %v; want sanitized path error", out, err)
		}
		for _, r := range string(out) {
			if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f || r >= 0x80 && r <= 0x9f {
				t.Fatalf("proxy error emitted terminal control U+%04X: %q", r, out)
			}
		}
	})
	t.Run("relative XDG cannot execute repository token command", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("token command fixture requires a POSIX shell")
		}
		root := t.TempDir()
		home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
		if err := os.MkdirAll(filepath.Join(repo, "gh-autoprofile"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(home, 0700); err != nil {
			t.Fatal(err)
		}
		broker, marker := filepath.Join(repo, "broker"), filepath.Join(root, "executed")
		if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf executed > \"$TEST_RELATIVE_XDG_MARKER\"\nprintf 'attacker-token\\n'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		pins := fmt.Sprintf("pins:\n  - user: attacker\n    owner: example-org\n    token_command: %q\n", broker)
		if err := os.WriteFile(filepath.Join(repo, "gh-autoprofile", "pins.yml"), []byte(pins), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("GH_AUTOACCOUNT_USER", "")
		t.Setenv("TEST_RELATIVE_XDG_MARKER", marker)
		for _, tc := range []struct {
			name, configHome, want string
			runsBroker             bool
		}{
			{name: "absolute override", configHome: repo, want: "username=x-access-token\npassword=attacker-token\n\n", runsBroker: true},
			{name: "relative override", configHome: "."},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("XDG_CONFIG_HOME", tc.configHome)
				for _, helper := range []string{"__git-credential", "credential-helper"} {
					cmd := exec.Command(binary, helper, "get")
					cmd.Dir = repo
					cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=example-org/repo.git\n\n")
					out, err := cmd.CombinedOutput()
					if err != nil || string(out) != tc.want {
						t.Fatalf("%s = %q, %v; want %q", helper, out, err, tc.want)
					}
					_, statErr := os.Stat(marker)
					if tc.runsBroker {
						if statErr != nil {
							t.Fatalf("%s did not execute the explicitly configured broker: %v", helper, statErr)
						}
						if err := os.Remove(marker); err != nil {
							t.Fatal(err)
						}
					} else if !os.IsNotExist(statErr) {
						t.Fatalf("%s executed the repository token command: %v", helper, statErr)
					}
				}
			})
		}
	})
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
