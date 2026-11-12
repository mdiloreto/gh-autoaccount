package gitidentity

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func isolatedGit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	home := t.TempDir()
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_CONFIG", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_PARAMETERS", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "global.gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	git(t, home, "config", "--global", "user.name", "Default User")
	git(t, home, "config", "--global", "user.email", "default@example.test")
	return home
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func TestIdentityRoundtrip(t *testing.T) {
	home := isolatedGit(t)
	root := filepath.Join(home, "vscode")
	nested := filepath.Join(root, "client")
	sibling := filepath.Join(home, "vscode-work", "repo")
	for _, repo := range []string{root, nested, sibling} {
		git(t, home, "init", "--quiet", repo)
	}
	localPath := filepath.Join(nested, ".git", "config")
	before, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(home, "other.gitconfig")
	if err := os.WriteFile(other, []byte("[alias]\n  preserved = status\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, home, "config", "--global", "--add", "include.path", other)
	registry := &config.PinRegistry{Pins: []config.Pin{
		{Dir: nested, GitName: "Nested User", GitEmail: "nested@example.test"},
		{Dir: root, GitName: "Parent User", GitEmail: "parent@example.test"},
	}}
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("identity must be opt-in")
	}
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	sync := func() {
		t.Helper()
		if err := Sync(registry); err != nil {
			t.Fatal(err)
		}
		if !CheckRegistered() {
			t.Fatal("managed include not uniquely registered")
		}
	}
	check := func(repo, identity string) {
		t.Helper()
		for _, variable := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
			if got := git(t, repo, "var", variable); !strings.HasPrefix(got, identity+" ") {
				t.Fatalf("%s in %s = %q, want %q", variable, repo, got, identity)
			}
		}
	}
	sync()
	git(t, home, "config", "--global", "--add", "include.path", path)
	sync()
	check(root, "Parent User <parent@example.test>")
	check(nested, "Nested User <nested@example.test>")
	check(sibling, "Default User <default@example.test>")
	if got := git(t, root, "config", "alias.preserved"); got != "status" {
		t.Fatal("unrelated include lost")
	}
	after, _ := os.ReadFile(localPath)
	if string(before) != string(after) {
		t.Fatal("repository-local config was modified")
	}
	git(t, nested, "config", "--local", "user.name", "Local User")
	check(nested, "Local User <nested@example.test>")
	git(t, nested, "config", "--local", "--unset", "user.name")
	t.Run("environment override", func(t *testing.T) {
		t.Setenv("GIT_AUTHOR_EMAIL", "env@example.test")
		if got := git(t, nested, "var", "GIT_AUTHOR_IDENT"); !strings.Contains(got, "<env@example.test>") {
			t.Fatal(got)
		}
	})
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "pin-*.gitconfig"))
	if len(files) != 2 {
		t.Fatalf("want two fragments, got %v", files)
	}
	keep := filepath.Join(filepath.Dir(path), "user.gitconfig")
	if err := os.WriteFile(keep, []byte("# user owned\n"), 0600); err != nil {
		t.Fatal(err)
	}
	registry.RemovePin(nested)
	sync()
	check(nested, "Parent User <parent@example.test>")
	files, _ = filepath.Glob(filepath.Join(filepath.Dir(path), "pin-*.gitconfig"))
	if len(files) != 2 {
		t.Fatalf("superseded fragments must be retained for in-flight readers: %v", files)
	}
	registry.RemovePin(root)
	sync()
	check(root, "Default User <default@example.test>")
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("user file was removed")
	}
	registry.AddPin(config.Pin{Dir: root, GitName: "Restored", GitEmail: "restored@example.test"})
	sync()
	check(root, "Restored <restored@example.test>")
	// A linked worktree is matched by its administrative gitdir, not checkout path.
	git(t, root, "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "initial")
	linked := filepath.Join(home, "outside-worktree")
	git(t, root, "worktree", "add", "--quiet", "-b", "linked", linked)
	check(linked, "Restored <restored@example.test>")
}

func TestIdentityGitEncoding(t *testing.T) {
	home := isolatedGit(t)
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "root [x]*? $() \"quotes\" \\ slash")
	git(t, home, "init", "--quiet", root)
	name := "  A \"quoted\" \\ name\nwith\ttab #; $(touch should-not-exist)  "
	email := "quote\"\\#;@example.test"
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: root, GitName: name, GitEmail: email}}}
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	if got := git(t, root, "config", "user.name"); got != name {
		t.Fatalf("name roundtrip: got %q, want %q", got, name)
	}
	if got := git(t, root, "config", "user.email"); got != email {
		t.Fatalf("email roundtrip: got %q, want %q", got, email)
	}
	// Account-only descendants must not erase the native identity inherited above.
	child := filepath.Join(root, "child")
	git(t, home, "init", "--quiet", child)
	registry.AddPin(config.Pin{Dir: child, User: "account-only"})
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	if got := git(t, child, "config", "user.email"); got != email {
		t.Fatal(got)
	}
}

func TestIdentityEffectiveGlobalXDG(t *testing.T) {
	home := isolatedGit(t)
	os.Unsetenv("GIT_CONFIG_GLOBAL")
	xdgGit := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git")
	if err := os.MkdirAll(xdgGit, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(xdgGit, "config")
	if err := os.WriteFile(path, []byte("# preserve me\n[user]\n  name = XDG\n  email = xdg@example.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	if err := Sync(&config.PinRegistry{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# preserve me") || !strings.Contains(string(data), "include.gitconfig") {
		t.Fatalf("XDG global not preserved/updated: %s", data)
	}
	if _, err := os.Stat(filepath.Join(home, ".gitconfig")); !os.IsNotExist(err) {
		t.Fatal("created ~/.gitconfig instead of updating effective XDG global")
	}
}

func TestPrepareGlobalWindowsUsesHome(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific Git home resolution")
	}
	home, profile := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", profile)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GIT_CONFIG_GLOBAL", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	checkGlobal := func(t *testing.T, want string) {
		t.Helper()
		global, lock, err := prepareGlobal(filepath.Join(home, "include.gitconfig"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(lock); err != nil {
			t.Fatal(err)
		}
		// prepareGlobal resolves an existing config through any symlinks; the
		// Windows runner's temp path uses an 8.3 short name, so resolve the
		// expectation the same way before comparing.
		if resolved, err := filepath.EvalSymlinks(want); err == nil {
			want = resolved
		}
		if !strings.EqualFold(global, want) {
			t.Fatalf("global = %q, want %q", global, want)
		}
	}
	t.Run("gitconfig", func(t *testing.T) {
		checkGlobal(t, filepath.Join(home, ".gitconfig"))
	})
	t.Run("XDG fallback", func(t *testing.T) {
		xdg := filepath.Join(home, ".config", "git", "config")
		if err := os.MkdirAll(filepath.Dir(xdg), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(xdg, []byte("# XDG config\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(home, ".gitconfig")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		checkGlobal(t, xdg)
	})
}

func TestPrepareGlobalIgnoresRelativeXDGConfigHome(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, tc := range []struct {
		name            string
		homeGit, xdgGit bool
		wantXDG         bool
	}{
		{name: "default gitconfig"},
		{name: "existing default XDG", xdgGit: true, wantXDG: true},
		{name: "gitconfig takes precedence", homeGit: true, xdgGit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, cwd := t.TempDir(), t.TempDir()
			var err error
			home, err = filepath.EvalSymlinks(home)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			if got, err := os.UserHomeDir(); err != nil || got != home {
				t.Skipf("os.UserHomeDir does not use HOME or USERPROFILE: %q, %v", got, err)
			}
			t.Setenv("XDG_CONFIG_HOME", ".")
			t.Setenv("XDG_DATA_HOME", "")
			t.Setenv("GH_CONFIG_DIR", "")
			t.Setenv("GIT_CONFIG_GLOBAL", "")
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_CONFIG_COUNT", "0")
			t.Setenv("GIT_CONFIG_PARAMETERS", "")
			t.Chdir(cwd)
			foreign := filepath.Join(cwd, "git", "config")
			if err := os.MkdirAll(filepath.Dir(foreign), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(foreign, []byte("# repository config\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.homeGit {
				if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("# home\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.xdgGit {
				xdg := filepath.Join(home, ".config", "git", "config")
				if err := os.MkdirAll(filepath.Dir(xdg), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(xdg, []byte("# xdg\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Startup normalization unsets the relative XDG config root.
			if err := config.NormalizeEnv(); err != nil {
				t.Fatal(err)
			}
			global, lock, err := prepareGlobal(filepath.Join(home, "include.gitconfig"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(lock) })
			want := filepath.Join(home, ".gitconfig")
			if tc.wantXDG {
				want = filepath.Join(home, ".config", "git", "config")
			}
			if !config.SamePath(global, want) {
				t.Fatalf("global = %q, want %q", global, want)
			}
			if _, err := os.Stat(foreign + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("repository config was locked: %v", err)
			}
		})
	}
}

func TestPrepareGlobalKeepsExplicitRelativeOverride(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	cwd := t.TempDir()
	t.Setenv("HOME", "relative-home")
	t.Setenv("USERPROFILE", "relative-home")
	t.Setenv("XDG_CONFIG_HOME", ".")
	t.Setenv("GIT_CONFIG_GLOBAL", "custom.gitconfig")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Chdir(cwd)
	global, lock, err := prepareGlobal(filepath.Join(cwd, "include.gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(lock) })
	if global != "custom.gitconfig" {
		t.Fatalf("global = %q, want explicit relative GIT_CONFIG_GLOBAL", global)
	}
}

func TestIdentityFailedGenerationPreservesPublishedConfig(t *testing.T) {
	home := isolatedGit(t)
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, GitName: "Original", GitEmail: "original@example.test"}}}
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	before, _ := os.ReadFile(path)
	registry.Pins[0].GitName = "Updated"
	registry.Pins = append(registry.Pins, config.Pin{Dir: filepath.Join(home, "invalid"), GitEmail: "nul\x00value"})
	if err := Sync(registry); err == nil {
		t.Fatal("expected invalid value to fail")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed generation changed published include")
	}
	git(t, home, "init", "--quiet", home)
	if got := git(t, home, "var", "GIT_AUTHOR_IDENT"); !strings.HasPrefix(got, "Original <original@example.test>") {
		t.Fatalf("published identity changed on failure: %s", got)
	}
	registry.Pins = registry.Pins[:1]
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Fatal("generation is not deterministic")
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	for _, file := range files {
		if strings.HasPrefix(file.Name(), ".identity-") {
			t.Fatalf("temporary file leaked: %s", file.Name())
		}
		info, err := file.Info()
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("generated file permissions: %v %v", info, err)
		}
	}
}

func TestIdentityIncludeEvaluatedLast(t *testing.T) {
	home := isolatedGit(t)
	other := filepath.Join(home, "other.gitconfig")
	git(t, home, "config", "--file", other, "alias.preserved", "status")
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	if err := os.Remove(global); err != nil {
		t.Fatal(err)
	}
	git(t, home, "config", "--global", "include.path", other)
	git(t, home, "config", "--global", "user.name", "Default")
	git(t, home, "config", "--global", "user.email", "default@example.test")
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	git(t, home, "init", "--quiet", home)
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, GitName: "Pinned", GitEmail: "pinned@example.test"}}}
	path, _ := Path()
	for i := 0; i < 3; i++ {
		if err := Sync(registry); err != nil {
			t.Fatal(err)
		}
		if !CheckRegistered() {
			t.Fatal("include not uniquely registered")
		}
		for _, variable := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
			if got := git(t, home, "var", variable); !strings.HasPrefix(got, "Pinned <pinned@example.test>") {
				t.Fatalf("iteration %d: %s", i, got)
			}
		}
		if got := git(t, home, "config", "alias.preserved"); got != "status" {
			t.Fatal("lost unrelated include")
		}
		// Even a section appended after registration is moved before our include on retry.
		f, err := os.OpenFile(global, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("\n[user]\n  name = Later Default\n  email = later@example.test\n")
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		git(t, home, "config", "--global", "--add", "include.path", path)
	}
}

func TestIdentityLockedGlobalPreservesPublication(t *testing.T) {
	home := isolatedGit(t)
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, GitName: "Original", GitEmail: "original@example.test"}}}
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	before, _ := os.ReadFile(path)
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	globalBefore, _ := os.ReadFile(global)
	if err := os.WriteFile(global+".lock", []byte("another git writer"), 0600); err != nil {
		t.Fatal(err)
	}
	registry.Pins[0].GitName = "Changed"
	if err := Sync(registry); err == nil {
		t.Fatal("ignored global Git lock")
	}
	after, _ := os.ReadFile(path)
	globalAfter, _ := os.ReadFile(global)
	lock, _ := os.ReadFile(global + ".lock")
	if string(before) != string(after) || string(globalBefore) != string(globalAfter) || string(lock) != "another git writer" {
		t.Fatal("failed Sync modified published config or another writer's lock")
	}
}

func TestIdentityOldIncludeStillReadable(t *testing.T) {
	home := isolatedGit(t)
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	git(t, home, "init", "--quiet", home)
	registry := &config.PinRegistry{Pins: []config.Pin{{Dir: home, GitName: "Original", GitEmail: "original@example.test"}}}
	if err := Sync(registry); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	old, _ := os.ReadFile(path)
	oldPath := filepath.Join(home, "reader-snapshot.gitconfig")
	if err := os.WriteFile(oldPath, old, 0600); err != nil {
		t.Fatal(err)
	}
	for _, pins := range [][]config.Pin{{{Dir: home, GitName: "Changed", GitEmail: "changed@example.test"}}, nil} {
		registry.Pins = pins
		if err := Sync(registry); err != nil {
			t.Fatal(err)
		}
		if got := git(t, home, "config", "--file", oldPath, "--includes", "user.email"); got != "original@example.test" {
			t.Fatalf("reader of old include lost identity: %s", got)
		}
	}
}

func TestIdentitySymlinkedGlobalUsesTargetLock(t *testing.T) {
	home := isolatedGit(t)
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	target := filepath.Join(home, "actual.gitconfig")
	if err := os.Rename(global, target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, global); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".lock", []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Sync(&config.PinRegistry{}); err == nil {
		t.Fatal("did not honor symlink target lock")
	}
	if err := os.Remove(target + ".lock"); err != nil {
		t.Fatal(err)
	}
	if err := Sync(&config.PinRegistry{}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(global); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("global symlink was replaced")
	}
	if !CheckRegistered() {
		t.Fatal("target registration missing")
	}
}

func TestIdentityDanglingGlobalSymlinkPreserved(t *testing.T) {
	for _, location := range []string{"override", "home", "xdg"} {
		t.Run(location, func(t *testing.T) {
			home := isolatedGit(t)
			global := os.Getenv("GIT_CONFIG_GLOBAL")
			if location != "override" {
				os.Unsetenv("GIT_CONFIG_GLOBAL")
				global = filepath.Join(home, ".gitconfig")
				if location == "xdg" {
					global = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "config")
					if err := os.MkdirAll(filepath.Dir(global), 0700); err != nil {
						t.Fatal(err)
					}
				}
			} else if err := os.Remove(global); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, "missing.gitconfig")
			if err := os.Symlink(target, global); err != nil {
				t.Fatal(err)
			}
			if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
				t.Fatal(err)
			}
			if err := Sync(&config.PinRegistry{}); err == nil {
				t.Fatal("dangling global symlink accepted")
			}
			if got, err := os.Readlink(global); err != nil || got != target {
				t.Fatalf("dangling symlink changed: %q %v", got, err)
			}
			include, _ := Path()
			for _, path := range []string{target, global + ".lock", include} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("failed registration created %s: %v", path, err)
				}
			}
		})
	}
}
