package profileexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestParseRepoSpec(t *testing.T) {
	t.Setenv("GH_HOST", "")
	for _, tc := range []struct {
		spec     string
		host     string
		owner    string
		wantNone bool
	}{
		{spec: "work-org/x", host: "github.com", owner: "work-org"},
		{spec: "ghe.example.test/work-org/x", host: "ghe.example.test", owner: "work-org"},
		{spec: "GHE.Example.Test/Work-Org/x", host: "ghe.example.test", owner: "Work-Org"},
		{spec: "https://github.com/work-org/x", host: "github.com", owner: "work-org"},
		{spec: "https://github.com/work-org/x.git", host: "github.com", owner: "work-org"},
		{spec: "https://x-access-token:secret@github.com/work-org/x.git", host: "github.com", owner: "work-org"},
		{spec: "https://ghe.example.test:8443/work-org/x", host: "ghe.example.test", owner: "work-org"},
		{spec: "git@github.com:work-org/x.git", host: "github.com", owner: "work-org"},
		{spec: "github.com:work-org/x", host: "github.com", owner: "work-org"},
		{spec: "ssh://git@github.com/work-org/x.git", host: "github.com", owner: "work-org"},
		{spec: "ssh://git@ghe.example.test:2222/work-org/x", host: "ghe.example.test", owner: "work-org"},
		{spec: "x", wantNone: true},
		{spec: "", wantNone: true},
		{spec: "a/b/c/d", wantNone: true},
		{spec: "/x", wantNone: true},
		{spec: "/work-org/x", wantNone: true},
		{spec: "https:///work-org/x", wantNone: true},
		{spec: "https://github.com//x", wantNone: true},
		{spec: "work-org/", wantNone: true},
		{spec: "https://github.com/work-org", wantNone: true},
		{spec: "ftp://github.com/work-org/x", wantNone: true},
		{spec: "not a host/work-org/x", wantNone: true},
	} {
		got, ok := parseRepoSpec(tc.spec, "github.com")
		if tc.wantNone {
			if ok {
				t.Errorf("parseRepoSpec(%q) = %+v, want none", tc.spec, got)
			}
			continue
		}
		if !ok || got.Host != tc.host || got.Owner != tc.owner {
			t.Errorf("parseRepoSpec(%q) = %+v %v, want %s/%s", tc.spec, got, ok, tc.host, tc.owner)
		}
	}
	if got, ok := parseRepoSpec("work-org/x", "ghe.example.test"); !ok || got.Host != "ghe.example.test" {
		t.Errorf("default host not applied: %+v", got)
	}
}

func TestRepoFlagForms(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		want  string
		found bool
	}{
		{[]string{"pr", "list", "-R", "a/b"}, "a/b", true},
		{[]string{"pr", "list", "--repo", "a/b"}, "a/b", true},
		{[]string{"pr", "list", "--repo=a/b"}, "a/b", true},
		{[]string{"pr", "list", "-R=a/b"}, "a/b", true},
		{[]string{"pr", "list", "-Ra/b"}, "a/b", true},
		{[]string{"--repo", "https://github.com/a/b"}, "https://github.com/a/b", true},
		{[]string{"pr", "list", "-R"}, "", true},
		// A candidate right after another bare flag may be that flag's value.
		{[]string{"issue", "create", "-t", "-Rwork-org/x", "-b", "body"}, "", false},
		{[]string{"issue", "create", "--body", "--repo", "work-org/x"}, "", false},
		{[]string{"issue", "create", "-t", "-R", "work-org/x"}, "", false},
		{[]string{"issue", "create", "-wR", "work-org/x"}, "", false},
		{[]string{"issue", "create", "-t", "--repo=work-org/x"}, "", false},
		{[]string{"issue", "create", "-t", "-R=work-org/x"}, "", false},
		{[]string{"issue", "create", "title", "--repo=work-org/x"}, "work-org/x", true},
		{[]string{"issue", "create", "title", "-R=work-org/x"}, "work-org/x", true},
		{[]string{"--repo=work-org/x", "-t", "title"}, "work-org/x", true},
		{[]string{"issue", "create", "--title=t", "-R", "work-org/x"}, "work-org/x", true},
		{[]string{"issue", "create", "-t", "title", "-Rwork-org/x"}, "work-org/x", true},
		{[]string{"issue", "create", "-t", "-Rbad", "--repo", "work-org/x"}, "", false},
		{[]string{"-R", "a/b", "-t", "-Rc/d"}, "a/b", true},
		{[]string{"pr", "list", "--", "-R", "a/b"}, "", false},
		{[]string{"pr", "list", "--state", "open"}, "", false},
		{[]string{"api", "--method", "GET"}, "", false},
		{nil, "", false},
	} {
		got, found := repoFlag(tc.args)
		if got != tc.want || found != tc.found {
			t.Errorf("repoFlag(%v) = %q, %v; want %q, %v", tc.args, got, found, tc.want, tc.found)
		}
	}
}

func TestCloneTarget(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		want  string
		found bool
	}{
		{[]string{"repo", "clone", "a/b"}, "a/b", true},
		{[]string{"repo", "clone", "a/b", "dest"}, "a/b", true},
		{[]string{"repo", "clone", "--upstream-remote-name", "up", "a/b"}, "a/b", true},
		{[]string{"repo", "clone", "-u", "up", "https://github.com/a/b"}, "https://github.com/a/b", true},
		{[]string{"repo", "clone", "--upstream-remote-name=up", "a/b"}, "a/b", true},
		{[]string{"repo", "clone", "bare"}, "bare", true},
		{[]string{"repo", "clone"}, "", false},
		{[]string{"repo", "clone", "--", "--depth=1"}, "", false},
		{[]string{"repo", "view", "a/b"}, "", false},
		{[]string{"clone", "a/b"}, "", false},
	} {
		got, found := cloneTarget(tc.args)
		if got != tc.want || found != tc.found {
			t.Errorf("cloneTarget(%v) = %q, %v; want %q, %v", tc.args, got, found, tc.want, tc.found)
		}
	}
}

// repoEnv isolates Git and gh environment so detection sees only the fixture.
func repoEnv(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CONFIG_PARAMETERS", "GH_REPO", "GH_HOST"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	return root
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func newRepo(t *testing.T, dir string, remotes [][2]string, resolved map[string]string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "--quiet")
	for _, remote := range remotes {
		runGit(t, dir, "remote", "add", remote[0], remote[1])
	}
	for name, value := range resolved {
		runGit(t, dir, "config", "remote."+name+".gh-resolved", value)
	}
	return dir
}

func TestRemoteRepoSelection(t *testing.T) {
	root := repoEnv(t)
	for _, tc := range []struct {
		name     string
		remotes  [][2]string
		resolved map[string]string
		want     string // host/owner, empty for none
	}{
		{"origin https", [][2]string{{"origin", "https://github.com/work-org/x.git"}}, nil, "github.com/work-org"},
		{"origin scp ssh", [][2]string{{"origin", "git@github.com:work-org/x.git"}}, nil, "github.com/work-org"},
		{"origin ssh url", [][2]string{{"origin", "ssh://git@github.com/work-org/x.git"}}, nil, "github.com/work-org"},
		{"enterprise host", [][2]string{{"origin", "git@ghe.example.test:work-org/x.git"}}, nil, "ghe.example.test/work-org"},
		{"origin beats others", [][2]string{{"fork", "https://github.com/me/x"}, {"origin", "https://github.com/work-org/x"}}, nil, "github.com/work-org"},
		{"upstream beats origin", [][2]string{{"origin", "https://github.com/Work-Org/r"}, {"upstream", "https://github.com/someone/r"}}, nil, "github.com/someone"},
		{"github beats origin", [][2]string{{"origin", "https://github.com/me/r"}, {"github", "https://github.com/work-org/r"}}, nil, "github.com/work-org"},
		{"upstream beats github", [][2]string{{"github", "https://github.com/me/r"}, {"upstream", "https://github.com/work-org/r"}}, nil, "github.com/work-org"},
		{"gh-resolved beats upstream", [][2]string{{"upstream", "https://github.com/me/r"}, {"fork", "https://github.com/work-org/r"}}, map[string]string{"fork": "base"}, "github.com/work-org"},
		{"only remote", [][2]string{{"upstream", "https://github.com/work-org/x"}}, nil, "github.com/work-org"},
		{"ambiguous without origin", [][2]string{{"a", "https://github.com/one/x"}, {"b", "https://github.com/two/x"}}, nil, ""},
		{"gh-resolved base beats origin", [][2]string{{"origin", "https://github.com/me/x"}, {"upstream", "https://github.com/work-org/x"}}, map[string]string{"upstream": "base"}, "github.com/work-org"},
		{"gh-resolved owner/repo", [][2]string{{"origin", "https://github.com/me/x"}}, map[string]string{"origin": "work-org/x"}, "github.com/work-org"},
		{"gh-resolved owner/repo keeps remote host", [][2]string{{"origin", "git@ghe.example.test:me/x.git"}}, map[string]string{"origin": "work-org/x"}, "ghe.example.test/work-org"},
		{"no remotes", nil, nil, ""},
		{"unparseable remote", [][2]string{{"origin", "/srv/git/x.git"}}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t, filepath.Join(root, strings.ReplaceAll(tc.name, " ", "-")), tc.remotes, tc.resolved)
			got, ok := (&remoteCache{dir: repo}).selected()
			if tc.want == "" {
				if ok {
					t.Fatalf("got %+v, want none", got)
				}
				return
			}
			if !ok || got.Host+"/"+got.Owner != tc.want {
				t.Fatalf("got %+v %v, want %s", got, ok, tc.want)
			}
		})
	}
	if _, ok := (&remoteCache{dir: root}).selected(); ok {
		t.Fatal("non-repository produced a remote")
	}
}

func TestDetectRepoSourcePrecedence(t *testing.T) {
	root := repoEnv(t)
	repo := newRepo(t, filepath.Join(root, "repo"), [][2]string{{"origin", "https://github.com/remote-org/x"}}, nil)
	args := []string{"repo", "clone", "clone-org/x"}
	for _, tc := range []struct {
		name  string
		query RepoQuery
		env   string
		want  string
	}{
		{"flag beats everything", RepoQuery{GH: true, Args: append([]string{"-R", "flag-org/x"}, args...)}, "env-org/x", "flag-org"},
		{"GH_REPO beats remote", RepoQuery{GH: true, Args: []string{"pr", "list"}}, "env-org/x", "env-org"},
		{"clone ignores GH_REPO", RepoQuery{GH: true, Args: args}, "env-org/x", "clone-org"},
		{"clone target beats remote", RepoQuery{GH: true, Args: args}, "", "clone-org"},
		{"ambiguous flag is skipped for GH_REPO", RepoQuery{GH: true, Args: []string{"issue", "create", "-t", "-Rflag-org/x"}}, "env-org/x", "env-org"},
		{"ambiguous flag is skipped for remote", RepoQuery{GH: true, Args: []string{"issue", "create", "-t", "-Rflag-org/x"}}, "", "remote-org"},
		{"remote last", RepoQuery{GH: true, Args: []string{"pr", "list"}}, "", "remote-org"},
		{"non-gh command ignores flag, env and clone", RepoQuery{Args: append([]string{"-R", "flag-org/x"}, args...)}, "env-org/x", "remote-org"},
		{"bare clone target falls to remote", RepoQuery{GH: true, Args: []string{"repo", "clone", "bare"}}, "env-org/x", "remote-org"},
		{"unparseable flag does not fall through", RepoQuery{GH: true, Args: []string{"-R", "bare"}}, "", ""},
		{"unparseable GH_REPO does not fall through", RepoQuery{GH: true}, "bare", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GH_REPO", tc.env)
			got, ok := detectRepo(&tc.query, &remoteCache{dir: repo})
			if tc.want == "" && ok || tc.want != "" && (!ok || got.Owner != tc.want) {
				t.Fatalf("got %+v %v, want owner %q", got, ok, tc.want)
			}
		})
	}
}

func TestDetectRepoDefaultHostFollowsGHHost(t *testing.T) {
	root := repoEnv(t)
	t.Setenv("GH_HOST", "ghe.example.test")
	got, ok := detectRepo(&RepoQuery{GH: true, Args: []string{"-R", "work-org/x"}}, &remoteCache{dir: root})
	if !ok || got.Host != "ghe.example.test" {
		t.Fatalf("got %+v %v", got, ok)
	}
}

// selectionFixture writes registry, settings and a fake gh printing tok-<user>.
func selectionFixture(t *testing.T, root string, pins []config.Pin) {
	t.Helper()
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\n[ \"$1 $2 $3\" = 'auth token --user' ] && printf 'tok-%s\\n' \"$4\" || exit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", gh)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	t.Setenv("GH_AUTOPROFILE_USER", "")
	t.Setenv("GH_AUTOPROFILE_HOST", "")
	if err := config.SavePins(&config.PinRegistry{Pins: pins}); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
}

func TestSelectionPrecedenceMatrix(t *testing.T) {
	root := repoEnv(t)
	pinned := filepath.Join(root, "work")
	unpinned := filepath.Join(root, "elsewhere")
	for _, dir := range []string{pinned, unpinned} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	selectionFixture(t, root, []config.Pin{
		{User: "bob", Dir: pinned, Mode: config.ModeWrapper},
		{User: "carol", Owner: "work-org", Mode: config.ModeWrapper},
	})
	ghRepo := func(spec string) *RepoQuery { return &RepoQuery{GH: true, Args: []string{"pr", "list", "-R", spec}} }
	for _, tc := range []struct {
		name       string
		dir        string
		policy     Policy
		wantUser   string
		wantSource string
		wantErr    string
	}{
		{"explicit beats owner and dir", pinned, Policy{User: "dave", Repo: ghRepo("work-org/x")}, "dave", "explicit", ""},
		{"owner beats dir", pinned, Policy{Repo: ghRepo("work-org/x")}, "carol", "owner", ""},
		{"owner is case insensitive", pinned, Policy{Repo: ghRepo("WORK-ORG/x")}, "carol", "owner", ""},
		{"owner beats default", unpinned, Policy{Repo: ghRepo("work-org/x")}, "carol", "owner", ""},
		{"unmatched owner falls to dir", pinned, Policy{Repo: ghRepo("other-org/x")}, "bob", "pin", ""},
		{"unmatched owner falls to default", unpinned, Policy{Repo: ghRepo("other-org/x")}, "alice", "default", ""},
		{"owner on another host does not match", unpinned, Policy{Repo: ghRepo("ghe.example.test/work-org/x")}, "alice", "default", ""},
		{"no repo query ignores owner pins", pinned, Policy{}, "bob", "pin", ""},
		{"dir only", pinned, Policy{Repo: &RepoQuery{GH: true}}, "bob", "pin", ""},
		{"default only", unpinned, Policy{Repo: &RepoQuery{GH: true}}, "alice", "default", ""},
		{"require-pin satisfied by owner pin", unpinned, Policy{RequirePin: true, Repo: ghRepo("work-org/x")}, "carol", "owner", ""},
		{"require-pin satisfied by dir pin", pinned, Policy{RequirePin: true, Repo: ghRepo("other/x")}, "bob", "pin", ""},
		{"require-pin rejects default", unpinned, Policy{RequirePin: true, Repo: ghRepo("other/x")}, "", "", "no pin found"},
		{"require-pin user matches owner pin", unpinned, Policy{RequirePin: true, User: "CAROL", Repo: ghRepo("work-org/x")}, "carol", "owner", ""},
		{"require-pin user must match owner pin", pinned, Policy{RequirePin: true, User: "bob", Repo: ghRepo("work-org/x")}, "", "", "does not match owner pin"},
		{"expect-user checks owner selection", pinned, Policy{ExpectUser: "bob", Repo: ghRepo("work-org/x")}, "", "", `selected profile is "carol"`},
		{"expect-user accepts owner selection", pinned, Policy{ExpectUser: "CAROL", Repo: ghRepo("work-org/x")}, "carol", "owner", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := ResolveContextWithPolicy(tc.dir, tc.policy)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ctx.SelectedUser != tc.wantUser || ctx.SelectionSource != tc.wantSource || ctx.Token != "tok-"+tc.wantUser {
				t.Fatalf("selection = %q/%q token %q, want %q/%q", ctx.SelectedUser, ctx.SelectionSource, ctx.Token, tc.wantUser, tc.wantSource)
			}
			if tc.wantSource == "owner" && (ctx.Pin == nil || !ctx.Pin.IsOwner()) {
				t.Fatalf("owner selection carries pin %+v", ctx.Pin)
			}
		})
	}
}

func TestSelectionFromWorkingDirectoryRemote(t *testing.T) {
	root := repoEnv(t)
	repo := newRepo(t, filepath.Join(root, "elsewhere", "repo"), [][2]string{{"origin", "git@github.com:work-org/x.git"}}, nil)
	selectionFixture(t, root, []config.Pin{
		{User: "bob", Dir: filepath.Join(root, "other"), Mode: config.ModeWrapper},
		{User: "carol", Owner: "work-org", Mode: config.ModeWrapper},
	})
	ctx, err := ResolveContextWithPolicy(repo, Policy{Repo: &RepoQuery{GH: true, Args: []string{"pr", "list"}}})
	if err != nil || ctx.SelectedUser != "carol" || ctx.SelectionSource != "owner" {
		t.Fatalf("remote selection: %+v %v", ctx, err)
	}
	runGit(t, repo, "remote", "set-url", "origin", "git@github.com:other-org/x.git")
	ctx, err = ResolveContextWithPolicy(repo, Policy{Repo: &RepoQuery{GH: true, Args: []string{"pr", "list"}}})
	if err != nil || ctx.SelectedUser != "alice" || ctx.SelectionSource != "default" {
		t.Fatalf("unmatched remote: %+v %v", ctx, err)
	}
}

// The remote lookup is the only extra cost: at most one git process, and none
// when no owner pin exists or an explicit source already named the owner.
func TestGitIsInvokedAtMostOnce(t *testing.T) {
	root := repoEnv(t)
	log := filepath.Join(root, "git.log")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$*\" >> \"$GIT_CALL_LOG\"\nprintf 'remote.origin.url\\nhttps://github.com/work-org/x.git\\000'\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_CALL_LOG", log)
	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	calls := func() int {
		data, _ := os.ReadFile(log)
		os.Remove(log)
		return strings.Count(string(data), "\n")
	}
	query := &RepoQuery{GH: true, Args: []string{"pr", "list"}}

	selectionFixture(t, root, []config.Pin{{User: "bob", Dir: dir, Mode: config.ModeWrapper}})
	if _, err := ResolveContextWithPolicy(dir, Policy{Repo: query}); err != nil || calls() != 0 {
		t.Fatalf("git ran without owner pins (err %v)", err)
	}

	selectionFixture(t, root, []config.Pin{{User: "bob", Dir: dir, Mode: config.ModeWrapper}, {User: "carol", Owner: "work-org", Mode: config.ModeWrapper}})
	ctx, err := ResolveContextWithPolicy(dir, Policy{Repo: query})
	if err != nil || ctx.SelectedUser != "carol" {
		t.Fatalf("selection: %+v %v", ctx, err)
	}
	if n := calls(); n != 1 {
		t.Fatalf("git ran %d times, want 1", n)
	}
	if _, err := ResolveContextWithPolicy(dir, Policy{Repo: &RepoQuery{GH: true, Args: []string{"-R", "work-org/x"}}}); err != nil || calls() != 0 {
		t.Fatalf("git ran despite -R (err %v)", err)
	}
	if _, err := ResolveContextWithPolicy(dir, Policy{User: "dave", Repo: query}); err != nil || calls() != 0 {
		t.Fatalf("git ran despite explicit user (err %v)", err)
	}
}

func TestOwnerPinEnvironmentUsesSelectedPin(t *testing.T) {
	root := repoEnv(t)
	selectionFixture(t, root, []config.Pin{{User: "carol", Owner: "work-org", Host: "ghe.example.test", Mode: config.ModeWrapper, GitEmail: "carol@example.test", GitName: "Carol", SSHKey: "/keys/carol"}})
	ctx, err := ResolveContextWithPolicy(root, Policy{Repo: &RepoQuery{GH: true, Args: []string{"-R", "ghe.example.test/work-org/x"}}})
	if err != nil || ctx.SelectionSource != "owner" || ctx.SelectedHost != "ghe.example.test" {
		t.Fatalf("selection: %+v %v", ctx, err)
	}
	env, err := BuildEnv(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"GH_AUTOPROFILE_USER": "carol", "GH_AUTOPROFILE_HOST": "ghe.example.test", "GH_AUTOPROFILE_MODE": "wrapper",
		"GH_ENTERPRISE_TOKEN": "tok-carol", "GIT_AUTHOR_EMAIL": "carol@example.test", "GIT_COMMITTER_NAME": "Carol",
		"GIT_SSH_COMMAND": "ssh -i '/keys/carol' -o IdentitiesOnly=yes",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestRemotesMappingToSeveralOwnerPinsAreReported(t *testing.T) {
	root := repoEnv(t)
	repo := newRepo(t, filepath.Join(root, "repo"), [][2]string{{"origin", "https://github.com/work-org/x"}, {"upstream", "git@github.com:Other-Org/x.git"}, {"fork", "https://github.com/me/x"}}, nil)
	selectionFixture(t, root, []config.Pin{
		{User: "bob", Owner: "work-org", Mode: config.ModeWrapper},
		{User: "carol", Owner: "other-org", Mode: config.ModeWrapper},
	})
	ctx, err := ResolveContextWithPolicy(repo, Policy{Repo: &RepoQuery{GH: true, Conflicts: true}})
	if err != nil || ctx.SelectedUser != "carol" || strings.Join(ctx.OwnerConflicts, ",") != "github.com/other-org,github.com/work-org" {
		t.Fatalf("conflicts: %+v %v", ctx, err)
	}
	if ctx, err = ResolveContextWithPolicy(repo, Policy{Repo: &RepoQuery{GH: true}}); err != nil || ctx.OwnerConflicts != nil {
		t.Fatalf("conflicts reported unasked: %+v %v", ctx, err)
	}
	runGit(t, repo, "remote", "remove", "upstream")
	if ctx, err = ResolveContextWithPolicy(repo, Policy{Repo: &RepoQuery{GH: true, Conflicts: true}}); err != nil || ctx.OwnerConflicts != nil {
		t.Fatalf("single owner reported as conflict: %+v %v", ctx, err)
	}
}
