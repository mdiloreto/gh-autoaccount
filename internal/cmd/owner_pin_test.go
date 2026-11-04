package cmd

import (
	"bytes"
	"encoding/json"
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
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

// ownerHome isolates a home whose fake gh answers `auth token --user X` with
// tok-X and otherwise reports the credential it was given.
func ownerHome(t *testing.T) string {
	t.Helper()
	home := onboardingHome(t)
	gh := filepath.Join(home, "bin", "gh")
	script := `#!/bin/sh
case "$*" in
  --version) printf 'gh version 2.50.0\n' ;;
  'auth status') printf 'github.com\n  Logged in to github.com account alice (keyring)\n' ;;
  'auth token --user '*) printf 'tok-%s\n' "$4" ;;
  *) printf 'ran token=%s%s args=%s\n' "$GH_TOKEN" "$GH_ENTERPRISE_TOKEN" "$*" ;;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	t.Setenv("GH_PATH", gh)
	t.Setenv("GH_AUTOPROFILE_DISABLE_SHIM", "")
	t.Setenv("GH_REPO", "")
	os.Unsetenv("GH_REPO")
	t.Setenv("GH_HOST", "")
	os.Unsetenv("GH_HOST")
	return home
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func repoWithRemote(t *testing.T, dir, remote string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "--quiet")
	if remote != "" {
		gitIn(t, dir, "remote", "add", "origin", remote)
	}
	return dir
}

func savePins(t *testing.T, pins ...config.Pin) {
	t.Helper()
	if err := config.SavePins(&config.PinRegistry{Pins: pins}); err != nil {
		t.Fatal(err)
	}
}

func runPinCmd(t *testing.T, args ...string) error {
	t.Helper()
	command := NewPinCmd()
	command.SetArgs(args)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	runErr := command.Execute()
	w.Close()
	r.Close()
	return runErr
}

func TestPinOwnerStoresRegistryEntryWithoutDirenv(t *testing.T) {
	home := ownerHome(t)
	// No shell library is installed: a directory pin would be refused here.
	if err := runPinCmd(t, "alice", "--owner", "Work-Org", "--git-email", "alice@example.test", "--git-name", "Alice"); err != nil {
		t.Fatal(err)
	}
	registry, err := config.LoadPins()
	if err != nil || len(registry.Pins) != 1 {
		t.Fatalf("registry: %+v %v", registry, err)
	}
	pin := registry.Pins[0]
	if pin.Owner != "Work-Org" || pin.Dir != "" || pin.Mode != config.ModeWrapper || pin.User != "alice" || pin.GitEmail != "alice@example.test" || pin.GitName != "Alice" {
		t.Fatalf("unexpected pin: %+v", pin)
	}
	if matches, _ := filepath.Glob(filepath.Join(home, "**", ".envrc")); len(matches) != 0 {
		t.Fatalf(".envrc written for an owner pin: %v", matches)
	}
	if err := runPinCmd(t, "alice", "--owner", "WORK-ORG"); err != nil {
		t.Fatal(err)
	}
	if registry, _ = config.LoadPins(); len(registry.Pins) != 1 || registry.Pins[0].Owner != "WORK-ORG" {
		t.Fatalf("re-pin must replace: %+v", registry.Pins)
	}
}

func TestPinOwnerRejectsInvalidCombinations(t *testing.T) {
	home := ownerHome(t)
	for name, args := range map[string][]string{
		"dir and owner":      {"alice", "--owner", "org", "--dir", home},
		"export token":       {"alice", "--owner", "org", "--export-token"},
		"invalid owner":      {"alice", "--owner", "bad owner"},
		"owner with slash":   {"alice", "--owner", "org/repo"},
		"invalid host":       {"alice", "--owner", "org", "--host", "not a host"},
		"missing ssh key":    {"alice", "--owner", "org", "--ssh-key", filepath.Join(home, "absent")},
		"relative token cmd": {"alice", "--owner", "org", "--host", "ghe.example.test", "--token-command", "relative"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := runPinCmd(t, args...); err == nil {
				t.Fatal("accepted")
			}
			if registry, err := config.LoadPins(); err != nil || len(registry.Pins) != 0 {
				t.Fatalf("registry changed: %+v %v", registry, err)
			}
		})
	}
}

func TestPinOwnerRefreshesHelpersUseHTTPPathAndUnpinRemoves(t *testing.T) {
	home := ownerHome(t)
	if err := gitcred.ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	broker := filepath.Join(home, "broker")
	if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf '%s\\n' broker-token\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runPinCmd(t, "dana", "--owner", "ghe-org", "--host", "GHE.example.test", "--token-command", broker); err != nil {
		t.Fatal(err)
	}
	if !gitcred.CheckConfiguredHost("ghe.example.test") || !gitcred.CheckUseHTTPPathHost("ghe.example.test") {
		t.Fatal("helper or useHttpPath not configured for the owner pin's host")
	}
	if gitcred.CheckUseHTTPPathHost("github.com") {
		t.Fatal("useHttpPath enabled on a host without owner pins")
	}
	registry, _ := config.LoadPins()
	if registry.Pins[0].Host != "ghe.example.test" {
		t.Fatalf("host not normalized: %+v", registry.Pins[0])
	}
	for _, args := range [][]string{{"--owner", "ghe-org"}, {"--host", "ghe.example.test"}, {"--owner", "other", "--host", "ghe.example.test"}, {"--owner", "ghe-org", home}} {
		command := NewUnpinCmd()
		command.SetArgs(args)
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		if err := command.Execute(); err == nil {
			t.Fatalf("unpin %v accepted", args)
		}
	}
	if registry, _ = config.LoadPins(); len(registry.Pins) != 1 {
		t.Fatalf("failed unpin changed the registry: %+v", registry.Pins)
	}
	command := NewUnpinCmd()
	command.SetArgs([]string{"--owner", "GHE-ORG", "--host", "ghe.example.test"})
	captureOnboarding(t, command.Execute)
	if registry, _ = config.LoadPins(); len(registry.Pins) != 0 {
		t.Fatalf("owner pin not removed: %+v", registry.Pins)
	}
}

func TestListAndStatusShowOwnerPinsAndSource(t *testing.T) {
	home := ownerHome(t)
	work := filepath.Join(home, "work")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	savePins(t,
		config.Pin{User: "carol", Dir: work, Mode: config.ModeWrapper},
		config.Pin{User: "bob", Owner: "work-org", Mode: config.ModeWrapper, GitEmail: "bob@example.test"},
	)
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	owned := repoWithRemote(t, filepath.Join(home, "elsewhere", "owned"), "git@github.com:work-org/x.git")
	other := repoWithRemote(t, filepath.Join(home, "elsewhere", "other"), "git@github.com:other-org/x.git")
	inWork := repoWithRemote(t, filepath.Join(work, "repo"), "https://github.com/other-org/x")

	t.Chdir(owned)
	list := NewListCmd()
	var out bytes.Buffer
	list.SetOut(&out)
	list.SetArgs([]string{"--json"})
	if err := list.Execute(); err != nil {
		t.Fatal(err)
	}
	var listed struct {
		SchemaVersion int `json:"schema_version"`
		Pins          []map[string]any
	}
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil || listed.SchemaVersion != 1 || len(listed.Pins) != 2 {
		t.Fatalf("list json: %s %v", out.String(), err)
	}
	if listed.Pins[0]["directory"] != work || listed.Pins[0]["owner"] != nil {
		t.Fatalf("directory pin shape changed: %v", listed.Pins[0])
	}
	if _, hasDir := listed.Pins[1]["directory"]; hasDir || listed.Pins[1]["owner"] != "work-org" || listed.Pins[1]["user"] != "bob" || listed.Pins[1]["mode"] != "wrapper" {
		t.Fatalf("owner pin shape: %v", listed.Pins[1])
	}
	out.Reset()
	list = NewListCmd()
	list.SetOut(&out)
	if err := list.Execute(); err != nil {
		t.Fatal(err)
	}
	var selectedLine string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "* ") {
			selectedLine = line
		}
	}
	if !strings.Contains(out.String(), "owner:work-org") || !strings.Contains(selectedLine, "owner:work-org") {
		t.Fatalf("list text does not mark the selected owner pin:\n%s", out.String())
	}

	status := func(dir string) statusDiagnostics {
		t.Helper()
		command := NewStatusCmd()
		var buf bytes.Buffer
		command.SetOut(&buf)
		command.SetArgs([]string{"--json", "--dir", dir})
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
		var result statusDiagnostics
		if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
			t.Fatalf("status json: %s %v", buf.String(), err)
		}
		return result
	}
	for _, tc := range []struct{ name, dir, user, source, owner string }{
		{"owner remote", owned, "bob", "owner", "work-org"},
		{"other remote from default", other, "alice", "default", ""},
		{"owner remote beats nothing else", owned, "bob", "owner", "work-org"},
		{"dir pin with unrelated remote", inWork, "carol", "pin", ""},
	} {
		got := status(tc.dir)
		if got.SchemaVersion != 1 || got.SelectedUser != tc.user || got.SelectionSource != tc.source || !got.Authentication.CredentialAvailable {
			t.Errorf("%s: %+v", tc.name, got)
		}
		if tc.owner != "" && (got.Pin == nil || got.Pin.Owner != tc.owner) {
			t.Errorf("%s: pin %+v", tc.name, got.Pin)
		}
	}
	// The owner pin outranks the directory pin from inside the pinned directory too.
	owner := repoWithRemote(t, filepath.Join(work, "owned-repo"), "https://github.com/work-org/x.git")
	if got := status(owner); got.SelectedUser != "bob" || got.SelectionSource != "owner" {
		t.Errorf("owner must beat dir pin: %+v", got)
	}

	command := NewStatusCmd()
	command.SetArgs([]string{"--dir", owned})
	var text bytes.Buffer
	command.SetOut(&text)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Selected account: bob (source: owner)", "Pinned owner:     work-org", "Pinned email:     bob@example.test"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("status text missing %q:\n%s", want, text.String())
		}
	}
	if strings.Contains(text.String(), "Pinned directory:") {
		t.Errorf("status text shows a directory for an owner pin:\n%s", text.String())
	}
}

func runShim(t *testing.T, dir string, env map[string]string, args ...string) string {
	t.Helper()
	t.Chdir(dir)
	for key, value := range env {
		t.Setenv(key, value)
	}
	command := NewShimGHCMD()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		t.Fatalf("shim %v: %v", args, err)
	}
	return out.String()
}

func TestShimSelectsOwnerPinFromEverySource(t *testing.T) {
	home := ownerHome(t)
	work := filepath.Join(home, "work")
	unpinned := filepath.Join(home, "unpinned")
	for _, dir := range []string{work, unpinned} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	savePins(t,
		config.Pin{User: "carol", Dir: work, Mode: config.ModeWrapper},
		config.Pin{User: "bob", Owner: "work-org", Mode: config.ModeWrapper},
	)
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	sshRemote := repoWithRemote(t, filepath.Join(home, "r1"), "git@github.com:work-org/x.git")
	sshURLRemote := repoWithRemote(t, filepath.Join(home, "r2"), "ssh://git@github.com/work-org/x.git")
	httpsRemote := repoWithRemote(t, filepath.Join(home, "r3"), "https://github.com/work-org/x.git")
	resolved := repoWithRemote(t, filepath.Join(home, "r4"), "https://github.com/me/fork.git")
	gitIn(t, resolved, "remote", "add", "upstream", "https://github.com/work-org/x.git")
	gitIn(t, resolved, "config", "remote.upstream.gh-resolved", "base")
	sshRemoteOther := repoWithRemote(t, filepath.Join(home, "r7"), "git@github.com:other-org/x.git")
	upstreamFirst := repoWithRemote(t, filepath.Join(home, "r8"), "https://github.com/work-org/x.git")
	gitIn(t, upstreamFirst, "remote", "add", "upstream", "https://github.com/someone/x.git")
	otherInWork := repoWithRemote(t, filepath.Join(work, "r5"), "https://github.com/other-org/x.git")
	bare := repoWithRemote(t, filepath.Join(home, "r6"), "")
	for _, tc := range []struct {
		name string
		dir  string
		env  map[string]string
		args []string
		want string
	}{
		{"-R flag", unpinned, nil, []string{"pr", "list", "-R", "work-org/x"}, "tok-bob"},
		{"--repo flag", unpinned, nil, []string{"pr", "list", "--repo", "work-org/x"}, "tok-bob"},
		{"--repo= flag", unpinned, nil, []string{"pr", "list", "--repo=work-org/x"}, "tok-bob"},
		{"host qualified flag", unpinned, nil, []string{"pr", "list", "-R", "github.com/work-org/x"}, "tok-bob"},
		{"url flag", unpinned, nil, []string{"pr", "list", "-R", "https://github.com/work-org/x"}, "tok-bob"},
		{"flag beats remote", sshRemote, nil, []string{"pr", "list", "-R", "other-org/x"}, "tok-alice"},
		{"GH_REPO", unpinned, map[string]string{"GH_REPO": "work-org/x"}, []string{"pr", "list"}, "tok-bob"},
		{"flag beats GH_REPO", unpinned, map[string]string{"GH_REPO": "work-org/x"}, []string{"pr", "list", "-R", "other-org/x"}, "tok-alice"},
		{"repo clone owner/repo", unpinned, nil, []string{"repo", "clone", "work-org/x"}, "tok-bob"},
		{"repo clone url with destination", unpinned, nil, []string{"repo", "clone", "https://github.com/work-org/x", "dest", "--", "--depth=1"}, "tok-bob"},
		{"repo clone beats cwd dir pin", work, nil, []string{"repo", "clone", "work-org/x"}, "tok-bob"},
		{"repo clone elsewhere keeps dir pin", work, nil, []string{"repo", "clone", "other-org/x"}, "tok-carol"},
		{"repo clone bare name uses the remote", sshRemote, nil, []string{"repo", "clone", "x"}, "tok-bob"},
		{"repo clone ignores GH_REPO", unpinned, map[string]string{"GH_REPO": "work-org/x"}, []string{"repo", "clone", "other-org/x"}, "tok-alice"},
		{"value of another flag is not -R", sshRemoteOther, nil, []string{"issue", "create", "-t", "-Rwork-org/x", "-b", "body"}, "tok-alice"},
		{"value of --body is not --repo", sshRemoteOther, nil, []string{"issue", "create", "--body", "--repo", "work-org/x"}, "tok-alice"},
		{"--repo= after a flag is ambiguous", sshRemoteOther, nil, []string{"issue", "create", "-t", "--repo=work-org/x"}, "tok-alice"},
		{"-R= after a flag is ambiguous", sshRemoteOther, nil, []string{"issue", "create", "-t", "-R=work-org/x"}, "tok-alice"},
		{"--repo= after a positional is explicit", sshRemoteOther, nil, []string{"issue", "create", "title", "--repo=work-org/x"}, "tok-bob"},
		{"-R after a non-flag token is explicit", sshRemoteOther, nil, []string{"issue", "create", "title", "-R", "work-org/x"}, "tok-bob"},
		{"ambiguous -R falls to GH_REPO", sshRemoteOther, map[string]string{"GH_REPO": "work-org/x"}, []string{"issue", "create", "-t", "-Rother-org/x"}, "tok-bob"},
		{"upstream outranks origin", upstreamFirst, nil, []string{"pr", "list"}, "tok-alice"},
		{"empty host never matches", unpinned, nil, []string{"pr", "list", "-R", "/work-org/x"}, "tok-alice"},
		{"scp remote", sshRemote, nil, []string{"pr", "list"}, "tok-bob"},
		{"ssh url remote", sshURLRemote, nil, []string{"pr", "list"}, "tok-bob"},
		{"https remote", httpsRemote, nil, []string{"pr", "list"}, "tok-bob"},
		{"gh-resolved remote", resolved, nil, []string{"pr", "list"}, "tok-bob"},
		{"unrelated remote in dir pin", otherInWork, nil, []string{"pr", "list"}, "tok-carol"},
		{"no remote in dir pin", work, nil, []string{"pr", "list"}, "tok-carol"},
		{"no remote unpinned", bare, nil, []string{"pr", "list"}, "tok-alice"},
		{"not a repository", unpinned, nil, []string{"pr", "list"}, "tok-alice"},
		{"remote on another host does not match", unpinned, map[string]string{"GH_REPO": "ghe.example.test/work-org/x"}, []string{"pr", "list"}, "tok-alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runShim(t, tc.dir, tc.env, tc.args...); !strings.Contains(got, "token="+tc.want+" ") {
				t.Fatalf("got %q, want %s", got, tc.want)
			}
		})
	}
}

func runExecCmd(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	command := NewExecCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func TestExecSelectsOwnerPin(t *testing.T) {
	home := ownerHome(t)
	work := filepath.Join(home, "work")
	unpinned := filepath.Join(home, "unpinned")
	for _, dir := range []string{work, unpinned} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	savePins(t,
		config.Pin{User: "carol", Dir: work, Mode: config.ModeWrapper},
		config.Pin{User: "bob", Owner: "work-org", Mode: config.ModeWrapper},
	)
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	owned := repoWithRemote(t, filepath.Join(home, "owned"), "git@github.com:work-org/x.git")
	personal := repoWithRemote(t, filepath.Join(home, "personal"), "git@github.com:alice/p.git")
	sh := []string{"--", "sh", "-c", `printf 'token=%s user=%s' "$GH_TOKEN" "$GH_AUTOPROFILE_USER"`}
	for _, tc := range []struct {
		name    string
		dir     string
		args    []string
		want    string
		wantErr string
	}{
		{"gh -R", unpinned, []string{"--", "gh", "pr", "list", "-R", "work-org/x"}, "token=tok-bob ", ""},
		{"ambiguous -R cannot satisfy require-pin", personal, []string{"--require-pin", "--expect-user", "bob", "--", "gh", "issue", "create", "-t", "-Rwork-org/x"}, "", "no pin found"},
		{"ambiguous --repo= cannot satisfy require-pin", personal, []string{"--require-pin", "--expect-user", "bob", "--", "gh", "issue", "create", "-t", "--repo=work-org/x"}, "", "no pin found"},
		{"ambiguous -R= cannot satisfy require-pin", personal, []string{"--require-pin", "--expect-user", "bob", "--", "gh", "issue", "create", "-t", "-R=work-org/x"}, "", "no pin found"},
		{"gh repo clone", unpinned, []string{"--", "gh", "repo", "clone", "work-org/x"}, "token=tok-bob ", ""},
		{"non-gh child uses cwd remote", owned, sh, "token=tok-bob user=bob", ""},
		{"non-gh child ignores gh flags", unpinned, []string{"--", "sh", "-c", `printf 'token=%s' "$GH_TOKEN"`, "-R", "work-org/x"}, "token=tok-alice", ""},
		{"explicit user beats owner", unpinned, []string{"--user", "dave", "--", "gh", "pr", "list", "-R", "work-org/x"}, "token=tok-dave ", ""},
		{"require-pin satisfied by owner pin", unpinned, []string{"--require-pin", "--", "gh", "pr", "list", "-R", "work-org/x"}, "token=tok-bob ", ""},
		{"require-pin satisfied by owner remote", owned, append([]string{"--require-pin"}, sh...), "token=tok-bob user=bob", ""},
		{"require-pin unmatched", unpinned, []string{"--require-pin", "--", "gh", "pr", "list", "-R", "other-org/x"}, "", "no pin found"},
		{"require-pin with matching user", unpinned, []string{"--require-pin", "--user", "BOB", "--", "gh", "pr", "list", "-R", "work-org/x"}, "token=tok-bob ", ""},
		{"require-pin with mismatched user", work, []string{"--require-pin", "--user", "carol", "--", "gh", "pr", "list", "-R", "work-org/x"}, "", "does not match owner pin"},
		{"expect-user checks owner selection", work, []string{"--expect-user", "carol", "--", "gh", "pr", "list", "-R", "work-org/x"}, "", `selected profile is "bob"`},
		{"expect-user accepts owner selection", work, []string{"--expect-user", "BOB", "--", "gh", "pr", "list", "-R", "work-org/x"}, "token=tok-bob ", ""},
		{"unmatched owner keeps dir pin", work, []string{"--", "gh", "pr", "list", "-R", "other-org/x"}, "token=tok-carol ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runExecCmd(t, tc.dir, tc.args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || out != "" {
					t.Fatalf("got %q (%v), want error %q before running the child", out, err, tc.wantErr)
				}
				return
			}
			if err != nil || !strings.Contains(out, tc.want) {
				t.Fatalf("got %q (%v), want %q", out, err, tc.want)
			}
		})
	}
}

func TestSetupEnablesUseHTTPPathAndDoctorRepairsIt(t *testing.T) {
	ownerHome(t)
	savePins(t,
		config.Pin{User: "alice", Owner: "work-org", Mode: config.ModeWrapper},
		config.Pin{User: "dana", Owner: "ghe-org", Host: "ghe.example.test", Mode: config.ModeWrapper},
	)
	if out, err := exec.Command("git", "config", "--global", "user.name", "Unrelated").CombinedOutput(); err != nil {
		t.Fatalf("seed: %s %v", out, err)
	}
	if out, err := exec.Command("git", "config", "--global", "credential.https://github.com.useHttpPath", "false").CombinedOutput(); err != nil {
		t.Fatalf("seed: %s %v", out, err)
	}
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--no-shell-rc", "--credential-helper"})
	captureOnboarding(t, setup.Execute)
	for _, host := range []string{"github.com", "ghe.example.test"} {
		if !gitcred.CheckConfiguredHost(host) || !gitcred.CheckUseHTTPPathHost(host) {
			t.Fatalf("%s: helper or useHttpPath missing after setup", host)
		}
	}
	if out, err := exec.Command("git", "config", "--global", "user.name").Output(); err != nil || strings.TrimSpace(string(out)) != "Unrelated" {
		t.Fatalf("unrelated global config changed: %q %v", out, err)
	}
	if out := captureOnboarding(t, NewDoctorCmd().Execute); strings.Contains(out, "useHttpPath") || !strings.Contains(out, "OK   git credential helper configured") {
		t.Fatalf("healthy doctor output:\n%s", out)
	}

	if out, err := exec.Command("git", "config", "--global", "--unset-all", "credential.https://ghe.example.test.useHttpPath").CombinedOutput(); err != nil {
		t.Fatalf("unset: %s %v", out, err)
	}
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "WARN git credential.https://ghe.example.test.useHttpPath is not true") || strings.Contains(out, "OK   git credential helper configured") || strings.Contains(out, "github.com.useHttpPath is not true") {
		t.Fatalf("doctor did not report the missing setting:\n%s", out)
	}
	if err := exec.Command("git", "config", "--global", "credential.https://github.com.useHttpPath", "false").Run(); err != nil {
		t.Fatal(err)
	}
	doctor := NewDoctorCmd()
	doctor.SetArgs([]string{"--fix"})
	captureOnboarding(t, doctor.Execute)
	for _, host := range []string{"github.com", "ghe.example.test"} {
		if !gitcred.CheckUseHTTPPathHost(host) {
			t.Fatalf("doctor --fix did not restore useHttpPath for %s", host)
		}
	}
	if out := captureOnboarding(t, NewDoctorCmd().Execute); strings.Contains(out, "useHttpPath") {
		t.Fatalf("doctor still reports useHttpPath after repair:\n%s", out)
	}
}

func TestFailedNativeSetupRestoresUseHTTPPath(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := ownerHome(t)
	for _, seed := range [][]string{
		{"credential.https://github.com.useHttpPath", "false"},
		{"credential.https://github.com.helper", "prior-helper"},
	} {
		if out, err := exec.Command("git", "config", "--global", "--add", seed[0], seed[1]).CombinedOutput(); err != nil {
			t.Fatalf("seed: %s %v", out, err)
		}
	}
	before, err := gitcred.SnapshotHosts([]string{"github.com"})
	if err != nil {
		t.Fatal(err)
	}
	savePins(t, config.Pin{User: "alice", Owner: "work-org", Mode: config.ModeWrapper})
	trace := filepath.Join(home, "git-trace")
	t.Setenv("GIT_TRACE", trace)
	// Native setup configures the helper first; a directory where the shell hook
	// belongs then fails it, so the rollback has something to undo.
	hook, err := direnvlib.ShellHookPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hook, 0700); err != nil {
		t.Fatal(err)
	}
	setup := NewSetupCmd()
	setup.SetArgs([]string{"--native", "--no-shell-rc"})
	setup.SetOut(&bytes.Buffer{})
	setup.SetErr(&bytes.Buffer{})
	captured := captureSilently(t, func() { err = setup.Execute() })
	if err == nil {
		t.Fatalf("expected setup to fail:\n%s", captured)
	}
	data, _ := os.ReadFile(trace)
	if !strings.Contains(string(data), "--add credential.https://github.com.useHttpPath") {
		t.Fatalf("useHttpPath was never written, so nothing was rolled back: %s", data)
	}
	after, err := gitcred.SnapshotHosts([]string{"github.com"})
	if err != nil || !reflect.DeepEqual(before, after) || after["github.com"].UseHTTPPath[0] != "false" {
		t.Fatalf("setup rollback changed the host section: before %#v after %#v (%v)", before, after, err)
	}
}

func captureSilently(t *testing.T, run func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	run()
	os.Stdout = old
	w.Close()
	out := <-done
	r.Close()
	return out
}

func TestDoctorIgnoresUseHTTPPathWithoutOwnerPins(t *testing.T) {
	home := ownerHome(t)
	dir := filepath.Join(home, "work")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	savePins(t, config.Pin{User: "alice", Dir: dir, Mode: config.ModeWrapper})
	if err := gitcred.ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	if out := captureOnboarding(t, NewDoctorCmd().Execute); strings.Contains(out, "useHttpPath") {
		t.Fatalf("doctor warned about useHttpPath without owner pins:\n%s", out)
	}
	savePins(t, config.Pin{User: "alice", Dir: dir, Mode: config.ModeWrapper}, config.Pin{User: "bob", Owner: "work-org", Mode: config.ModeWrapper})
	if out := captureOnboarding(t, NewDoctorCmd().Execute); !strings.Contains(out, "github.com.useHttpPath is not true") {
		t.Fatalf("doctor missed useHttpPath with an owner pin:\n%s", out)
	}
}

func TestStatusWarnsWhenRemotesMatchSeveralOwnerPins(t *testing.T) {
	home := ownerHome(t)
	savePins(t,
		config.Pin{User: "bob", Owner: "work-org", Mode: config.ModeWrapper},
		config.Pin{User: "carol", Owner: "other-org", Mode: config.ModeWrapper},
	)
	repo := repoWithRemote(t, filepath.Join(home, "mixed"), "https://github.com/work-org/x.git")
	gitIn(t, repo, "remote", "add", "upstream", "https://github.com/other-org/x.git")
	command := NewStatusCmd()
	var buf bytes.Buffer
	command.SetOut(&buf)
	command.SetArgs([]string{"--json", "--dir", repo})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var got statusDiagnostics
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil || strings.Join(got.ConflictingOwnerPins, ",") != "github.com/other-org,github.com/work-org" {
		t.Fatalf("json: %s %v", buf.String(), err)
	}
	command = NewStatusCmd()
	buf.Reset()
	command.SetOut(&buf)
	command.SetArgs([]string{"--dir", repo})
	if err := command.Execute(); err != nil || !strings.Contains(buf.String(), "WARN: remotes of this checkout match several owner pins") {
		t.Fatalf("text: %s %v", buf.String(), err)
	}
	gitIn(t, repo, "remote", "remove", "upstream")
	command = NewStatusCmd()
	buf.Reset()
	command.SetOut(&buf)
	command.SetArgs([]string{"--json", "--dir", repo})
	if err := command.Execute(); err != nil || strings.Contains(buf.String(), "conflicting_owner_pins") {
		t.Fatalf("single owner flagged: %s %v", buf.String(), err)
	}
}
