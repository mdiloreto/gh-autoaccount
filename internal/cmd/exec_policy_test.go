package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestExecPolicy(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "gh-autoprofile")
	build := exec.Command("go", "build", "-o", binary, "./cmd/gh-autoaccount")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	pinned := filepath.Join(root, "pinned")
	nested := filepath.Join(pinned, "nested")
	other := filepath.Join(nested, "other")
	unpinned := filepath.Join(root, "unpinned")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{other, unpinned, binDir, filepath.Join(root, "home"), filepath.Join(root, "gh-config")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gitConfig := filepath.Join(root, "gitconfig")
	if err := os.WriteFile(gitConfig, nil, 0600); err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(binDir, "gh")
	child := filepath.Join(binDir, "child")
	for path, script := range map[string]string{
		gh: `#!/bin/sh
test "$#" = 6 || exit 91
test "$1 $2 $3 $5 $6" = 'auth token --user --hostname github.com' || exit 92
test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}" || exit 93
printf '%s\n' "$4" >> "$TEST_EXEC_POLICY_AUTH_LOG"
test "$TEST_EXEC_POLICY_TOKEN_FAILURE" != 1 || exit 94
printf 'stored-%s\n' "$4"
`,
		child: `#!/bin/sh
printf 'ran\n' >> "$TEST_EXEC_POLICY_CHILD_LOG"
printf 'profile=%s|%s|%s|%s\n' "$GH_AUTOPROFILE_USER" "$GH_AUTOPROFILE_MODE" "$GH_TOKEN" "$GITHUB_TOKEN"
printf 'cwd=%s\n' "$PWD"
printf 'identity=%s|%s|%s|%s|%s\n' "$GIT_AUTHOR_EMAIL" "$GIT_COMMITTER_EMAIL" "$GIT_AUTHOR_NAME" "$GIT_COMMITTER_NAME" "$GIT_SSH_COMMAND"
printf 'arg=<%s>\n' "$@"
IFS= read -r input
printf 'stdin=%s\n' "$input"
printf 'child stderr\n' >&2
exit "$TEST_EXEC_POLICY_EXIT_CODE"
`,
	} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"HOME":                           filepath.Join(root, "home"),
		"XDG_CONFIG_HOME":                filepath.Join(root, "config"),
		"GH_CONFIG_DIR":                  filepath.Join(root, "gh-config"),
		"PATH":                           binDir + string(os.PathListSeparator) + "/usr/bin:/bin",
		"GH_AUTOPROFILE_REAL_GH":         gh,
		"GH_AUTOPROFILE_DISABLE_SHIM":    "",
		"GH_AUTOPROFILE_USER":            "",
		"GH_AUTOPROFILE_MODE":            "",
		"GH_TOKEN":                       "synthetic-ambient",
		"GITHUB_TOKEN":                   "synthetic-github-ambient",
		"GIT_CONFIG_GLOBAL":              gitConfig,
		"GIT_CONFIG_SYSTEM":              os.DevNull,
		"GIT_CONFIG_NOSYSTEM":            "1",
		"GIT_CONFIG_COUNT":               "0",
		"GIT_CONFIG_PARAMETERS":          "",
		"GIT_TERMINAL_PROMPT":            "0",
		"GIT_ASKPASS":                    "/bin/false",
		"GIT_AUTHOR_EMAIL":               "caller@example.test",
		"GIT_COMMITTER_EMAIL":            "caller@example.test",
		"GIT_AUTHOR_NAME":                "Caller",
		"GIT_COMMITTER_NAME":             "Caller",
		"GIT_SSH_COMMAND":                "caller ssh",
		"TEST_EXEC_POLICY_TOKEN_FAILURE": "0",
	} {
		t.Setenv(key, value)
	}
	t.Setenv("GH_AUTOPROFILE_GIT_CONFIG_START", "")
	if err := os.Unsetenv("GH_AUTOPROFILE_GIT_CONFIG_START"); err != nil {
		t.Fatal(err)
	}
	sshKey := filepath.Join(root, "alice's key")
	registry := &config.PinRegistry{Pins: []config.Pin{
		{User: "alice", Dir: pinned, Mode: config.ModeExport, GitEmail: "alice@example.test", GitName: "Alice Example", SSHKey: sshKey},
		{User: "bob", Dir: other, Mode: config.ModeWrapper},
	}}

	for _, tc := range []struct {
		name, cwd, dir, marker, markerHost, session, wantUser, wantMode, wantToken, wantAuth, wantErr string
		flags                                                                                []string
		pinIdentity, tokenFailure, invalidRegistry                                           bool
		exitCode                                                                             int
	}{
		{name: "require pin rejects ambient", cwd: unpinned, flags: []string{"--require-pin"}, wantErr: "no pin found"},
		{name: "user cannot bypass require pin", cwd: unpinned, flags: []string{"--require-pin", "--user", "alice"}, wantErr: "no pin found"},
		{name: "matching override cannot bypass require pin", cwd: unpinned, flags: []string{"--require-pin", "--user", "alice", "--expect-user", "ALICE"}, wantErr: "no pin found"},
		{name: "require pin uses dir not cwd", dir: unpinned, flags: []string{"--require-pin"}, wantErr: "no pin found"},
		{name: "require pin with override uses dir", dir: unpinned, flags: []string{"--require-pin", "--user", "alice"}, wantErr: "no pin found"},
		{name: "expected pin matches case insensitively", flags: []string{"--expect-user", "ALIce"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "matching markers never take ambient", marker: "ALICE", markerHost: "github.com", flags: []string{"--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "mismatched host rejects ambient", marker: "alice", markerHost: "other.example.invalid", flags: []string{"--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "expected pin mismatch before credentials", flags: []string{"--expect-user", "bob"}, wantErr: "selected profile is \"alice\""},
		{name: "expected user rejects no selection", cwd: unpinned, flags: []string{"--expect-user", "alice"}, wantErr: "no profile selected"},
		{name: "ambient profile marker is not a selection", cwd: unpinned, marker: "alice", flags: []string{"--expect-user", "alice"}, wantErr: "no profile selected"},
		{name: "empty explicit user rejected", flags: []string{"--user="}, wantErr: "--user must not be empty"},
		{name: "empty expected user rejected", flags: []string{"--expect-user="}, wantErr: "--expect-user must not be empty"},
		{name: "empty expected user with override rejected", flags: []string{"--user", "alice", "--expect-user", ""}, wantErr: "--expect-user must not be empty"},
		{name: "override selected over pin", flags: []string{"--user", "bob", "--expect-user", "BOB"}, wantUser: "bob", wantMode: "wrapper", wantToken: "stored-bob", wantAuth: "bob\n"},
		{name: "expected pin cannot hide override mismatch", flags: []string{"--user", "bob", "--expect-user", "alice"}, wantErr: "selected profile is \"bob\""},
		{name: "matching override needs no pin", cwd: unpinned, flags: []string{"--user", "alice", "--expect-user", "ALICE"}, wantUser: "alice", wantMode: "wrapper", wantToken: "stored-alice", wantAuth: "alice\n"},
		{name: "require pin rejects different auth override", flags: []string{"--require-pin", "--user", "bob", "--expect-user", "bob"}, wantErr: "does not match directory pin"},
		{name: "require pin retains same user pin", flags: []string{"--require-pin", "--user", "ALICE"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "expected user forces named credential with matching ambient marker", marker: "alice", flags: []string{"--expect-user", "alice"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "named credential failure never falls back to ambient", marker: "alice", flags: []string{"--expect-user", "alice"}, tokenFailure: true, wantAuth: "alice\n", wantErr: "cannot resolve token"},
		{name: "most specific pin selected", cwd: other, flags: []string{"--expect-user", "BOB", "--require-pin"}, wantUser: "bob", wantMode: "wrapper", wantToken: "stored-bob", wantAuth: "bob\n"},
		{name: "dir selects pin and child cwd", cwd: unpinned, dir: nested, flags: []string{"--expect-user", "alice", "--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "relative dir selects pin and child cwd", cwd: pinned, dir: "nested", flags: []string{"--expect-user", "alice", "--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "require pin without markers uses stored", flags: []string{"--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "require pin alone preserves stored credential selection", marker: "bob", flags: []string{"--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "default unpinned preserves ambient", cwd: unpinned, wantToken: "synthetic-ambient"},
		{name: "default unpinned clears stale profile", cwd: unpinned, marker: "alice"},
		{name: "default export without markers uses stored", wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "default wrapper uses stored credential", cwd: other, wantUser: "bob", wantMode: "wrapper", wantToken: "stored-bob", wantAuth: "bob\n"},
		{name: "default explicit user bypasses pin lookup", cwd: unpinned, flags: []string{"--user", "alice"}, invalidRegistry: true, wantUser: "alice", wantMode: "wrapper", wantToken: "stored-alice", wantAuth: "alice\n"},
		{name: "matching expected override bypasses invalid registry", cwd: unpinned, flags: []string{"--user", "alice", "--expect-user", "ALICE", "--require-pin=false"}, invalidRegistry: true, wantUser: "alice", wantMode: "wrapper", wantToken: "stored-alice", wantAuth: "alice\n"},
		{name: "mismatched expected override rejects before credentials with invalid registry", flags: []string{"--user", "alice", "--expect-user", "bob", "--require-pin=false"}, invalidRegistry: true, wantErr: "selected profile is \"alice\""},
		{name: "guard rejects invalid registry before credentials", flags: []string{"--require-pin", "--user", "alice"}, invalidRegistry: true, wantErr: "cannot load pin registry"},
		{name: "require pin with matching expected override rejects invalid registry", flags: []string{"--require-pin", "--user", "alice", "--expect-user", "alice"}, invalidRegistry: true, wantErr: "cannot load pin registry"},
		{name: "explicit false require pin preserves default", cwd: unpinned, flags: []string{"--require-pin=false"}, wantToken: "synthetic-ambient"},
		{name: "default exit 42 and streams forwarded", cwd: other, wantUser: "bob", wantMode: "wrapper", wantToken: "stored-bob", wantAuth: "bob\n", exitCode: 42},
		{name: "session override selects over the pin", session: "bob", wantUser: "bob", wantMode: "wrapper", wantToken: "stored-bob", wantAuth: "bob\n"},
		{name: "session override in an unpinned directory", cwd: unpinned, session: "alice", wantUser: "alice", wantMode: "wrapper", wantToken: "stored-alice", wantAuth: "alice\n"},
		{name: "explicit user outranks the session override", session: "bob", flags: []string{"--user", "alice"}, wantUser: "alice", wantMode: "wrapper", wantToken: "stored-alice", wantAuth: "alice\n"},
		{name: "require pin rejects a different session override", session: "bob", flags: []string{"--require-pin"}, wantErr: `GH_AUTOACCOUNT_USER="bob" does not match directory pin "alice"`},
		{name: "require pin keeps the pin for a matching session override", session: "ALICE", flags: []string{"--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true},
		{name: "expected user checks the session override", session: "bob", flags: []string{"--expect-user", "alice"}, wantErr: "selected profile is \"bob\""},
		{name: "invalid session override rejected before credentials", session: "not a login", wantErr: "GH_AUTOACCOUNT_USER is not a valid GitHub login"},
		{name: "guarded exit 42 and streams forwarded", flags: []string{"--expect-user", "ALICE", "--require-pin"}, wantUser: "alice", wantMode: "export", wantToken: "stored-alice", wantAuth: "alice\n", pinIdentity: true, exitCode: 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := config.SavePins(registry); err != nil {
				t.Fatal(err)
			}
			if tc.invalidRegistry {
				path, err := config.PinsFilePath()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("pins: ["), 0600); err != nil {
					t.Fatal(err)
				}
			}
			logDir := t.TempDir()
			authLog, childLog := filepath.Join(logDir, "auth"), filepath.Join(logDir, "child")
			t.Setenv("TEST_EXEC_POLICY_AUTH_LOG", authLog)
			t.Setenv("TEST_EXEC_POLICY_CHILD_LOG", childLog)
			t.Setenv("TEST_EXEC_POLICY_EXIT_CODE", fmt.Sprint(tc.exitCode))
			t.Setenv("GH_AUTOPROFILE_USER", tc.marker)
			t.Setenv("GH_AUTOPROFILE_HOST", tc.markerHost)
			t.Setenv("GH_AUTOACCOUNT_USER", tc.session)
			if tc.tokenFailure {
				t.Setenv("TEST_EXEC_POLICY_TOKEN_FAILURE", "1")
			}
			args := append([]string{"exec"}, tc.flags...)
			if tc.dir != "" {
				args = append(args, "--dir", tc.dir)
			}
			args = append(args, "--", child, "", "space and 'quote'", "--expect-user=bob", "$(not-evaluated)")
			command := exec.Command(binary, args...)
			command.Dir = tc.cwd
			if command.Dir == "" {
				command.Dir = nested
			}
			command.Stdin = strings.NewReader("synthetic input\n")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			gotAuth, readErr := os.ReadFile(authLog)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if string(gotAuth) != tc.wantAuth {
				t.Fatalf("credential lookups = %q, want %q (error %v, stderr %q)", gotAuth, tc.wantAuth, err, stderr.String())
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(stderr.String(), tc.wantErr) || stdout.Len() != 0 {
					t.Fatalf("stdout %q, stderr %q, error %v; want rejection %q", stdout.String(), stderr.String(), err, tc.wantErr)
				}
				if _, statErr := os.Stat(childLog); !os.IsNotExist(statErr) {
					t.Fatalf("child must not run: stat error %v", statErr)
				}
				return
			}
			if tc.exitCode == 0 && err != nil {
				t.Fatalf("exec: %v, stderr %q", err, stderr.String())
			}
			if tc.exitCode != 0 {
				if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != tc.exitCode {
					t.Fatalf("exit error %v, want code %d", err, tc.exitCode)
				}
			}
			wantDir := command.Dir
			if tc.dir != "" {
				wantDir = tc.dir
				if !filepath.IsAbs(wantDir) {
					wantDir = filepath.Join(command.Dir, wantDir)
				}
			}
			identity := "caller@example.test|caller@example.test|Caller|Caller|caller ssh"
			if tc.pinIdentity {
				identity = "alice@example.test|alice@example.test|Alice Example|Alice Example|ssh -i '" + strings.ReplaceAll(sshKey, "'", "'\\''") + "' -o IdentitiesOnly=yes"
			}
			githubToken := tc.wantToken
			if tc.wantUser == "" && tc.wantToken == "synthetic-ambient" {
				githubToken = "synthetic-github-ambient"
			}
			want := fmt.Sprintf("profile=%s|%s|%s|%s\ncwd=%s\nidentity=%s\narg=<>\narg=<space and 'quote'>\narg=<--expect-user=bob>\narg=<$(not-evaluated)>\nstdin=synthetic input\n", tc.wantUser, tc.wantMode, tc.wantToken, githubToken, wantDir, identity)
			if stdout.String() != want || stderr.String() != "child stderr\n" {
				t.Fatalf("stdout %q, stderr %q; want stdout %q and child stderr", stdout.String(), stderr.String(), want)
			}
			if got, err := os.ReadFile(childLog); err != nil || string(got) != "ran\n" {
				t.Fatalf("child log %q, error %v", got, err)
			}
		})
	}
}

func TestExecPolicyHelp(t *testing.T) {
	command := NewExecCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"--require-pin", "--expect-user", "selected profile", "case-insensitive", "--verify-identity", "cannot replace it"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("help missing %q", text)
		}
	}
}
