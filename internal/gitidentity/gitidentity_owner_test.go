package gitidentity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestOwnerIdentityFollowsRemoteAndOutranksDirectory(t *testing.T) {
	home := isolatedGit(t)
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "work")
	registry := &config.PinRegistry{Pins: []config.Pin{
		// Owner pins listed first: emission order, not registry order, must decide.
		{User: "owner", Owner: "work-org", GitName: "Owner User", GitEmail: "owner@example.test"},
		{User: "ghe", Owner: "work-org", Host: "ghe.example.test", GitName: "GHE Owner", GitEmail: "ghe@example.test"},
		{User: "dir", Dir: root, GitName: "Dir User", GitEmail: "dir@example.test"},
		{User: "no-identity", Owner: "silent-org"},
	}}
	if err := Install(registry); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	git(t, home, "config", "--global", "--add", "include.path", path)
	if err := Install(registry); err != nil {
		t.Fatal(err)
	}
	if !CheckRegistered() {
		t.Fatal("include not uniquely registered")
	}
	repo := func(dir, remote string) string {
		t.Helper()
		git(t, home, "init", "--quiet", dir)
		if remote != "" {
			git(t, dir, "remote", "add", "origin", remote)
		}
		return dir
	}
	check := func(dir, want string) {
		t.Helper()
		for _, variable := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
			if got := git(t, dir, "var", variable); !strings.HasPrefix(got, want+" ") {
				t.Fatalf("%s in %s = %q, want %q", variable, dir, got, want)
			}
		}
	}
	for i, tc := range []struct{ remote, want string }{
		{"https://github.com/work-org/x.git", "Owner User <owner@example.test>"},
		{"https://github.com/Work-ORG/x", "Owner User <owner@example.test>"},
		{"https://GitHub.com/work-org/x", "Owner User <owner@example.test>"},
		{"git@github.com:work-org/x.git", "Owner User <owner@example.test>"},
		{"ssh://git@github.com/work-org/x.git", "Owner User <owner@example.test>"},
		{"https://ghe.example.test/work-org/x", "GHE Owner <ghe@example.test>"},
		{"git@ghe.example.test:work-org/x.git", "GHE Owner <ghe@example.test>"},
		{"https://github.com/other-org/x", "Dir User <dir@example.test>"},
		{"https://github.com/work-organization/x", "Dir User <dir@example.test>"},
		{"https://example.test/github.com/work-org/x", "Dir User <dir@example.test>"},
		{"", "Dir User <dir@example.test>"},
	} {
		check(repo(filepath.Join(root, "in-dir", string(rune('a'+i))), tc.remote), tc.want)
	}
	// Owner identity applies outside any pinned directory, which is the point.
	check(repo(filepath.Join(home, "elsewhere", "owned"), "https://github.com/work-org/x"), "Owner User <owner@example.test>")
	check(repo(filepath.Join(home, "elsewhere", "other"), "https://github.com/other-org/x"), "Default User <default@example.test>")
	check(repo(filepath.Join(home, "elsewhere", "bare"), ""), "Default User <default@example.test>")
	// An owner pin without identity contributes nothing.
	check(repo(filepath.Join(home, "elsewhere", "silent"), "https://github.com/silent-org/x"), "Default User <default@example.test>")
	// Repository-local config keeps overriding the managed identity.
	owned := filepath.Join(home, "elsewhere", "owned")
	git(t, owned, "config", "--local", "user.name", "Local User")
	check(owned, "Local User <owner@example.test>")
	// Removing the owner pin removes its identity.
	registry.Pins = registry.Pins[1:]
	if err := Install(registry); err != nil {
		t.Fatal(err)
	}
	check(repo(filepath.Join(home, "elsewhere", "after-removal"), "https://github.com/work-org/x"), "Default User <default@example.test>")
}

func fakeGitVersion(t *testing.T, version string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo '" + version + "'; exit 0; fi\nexit 99\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestOwnerIdentityRequiresHasconfigGit(t *testing.T) {
	isolatedGit(t)
	withIdentity := &config.PinRegistry{Pins: []config.Pin{{User: "o", Owner: "work-org", GitEmail: "o@example.test"}}}
	withoutIdentity := &config.PinRegistry{Pins: []config.Pin{{User: "o", Owner: "work-org"}, {User: "d", Dir: "/tmp/d", GitEmail: "d@example.test"}}}
	for _, tc := range []struct {
		version string
		wantErr bool
	}{
		{"git version 2.35.9", true},
		{"git version 2.30.2", true},
		{"git version 1.9.1", true},
		{"git version 2.36.0", false},
		{"git version 2.39.5 (Apple Git-154)", false},
		{"git version 2.43.0.windows.1", false},
		{"git version 3.0.0", false},
		{"unrecognizable", true},
	} {
		t.Run(tc.version, func(t *testing.T) {
			fakeGitVersion(t, tc.version)
			err := CheckOwnerIdentitySupport(withIdentity)
			if tc.wantErr != (err != nil) {
				t.Fatalf("CheckOwnerIdentitySupport = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && tc.version != "unrecognizable" && !strings.Contains(err.Error(), "2.36") {
				t.Fatalf("error does not name the required version: %v", err)
			}
			// Without owner identity the version is irrelevant: git must not even run.
			if err := CheckOwnerIdentitySupport(withoutIdentity); err != nil {
				t.Fatalf("unneeded check failed: %v", err)
			}
		})
	}
}

func TestOwnerIdentityRefusedOnOldGitWithoutChangingPublishedConfig(t *testing.T) {
	home := isolatedGit(t)
	registry := &config.PinRegistry{Pins: []config.Pin{{User: "d", Dir: home, GitName: "Dir User", GitEmail: "dir@example.test"}}}
	if err := Install(registry); err != nil {
		t.Fatal(err)
	}
	path, _ := Path()
	before, _ := os.ReadFile(path)
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	globalBefore, _ := os.ReadFile(global)
	fakeGitVersion(t, "git version 2.30.0")
	registry.Pins = append(registry.Pins, config.Pin{User: "o", Owner: "work-org", GitEmail: "o@example.test"})
	if err := Install(registry); err == nil || !strings.Contains(err.Error(), "2.36") {
		t.Fatalf("old Git accepted owner identity: %v", err)
	}
	after, _ := os.ReadFile(path)
	globalAfter, _ := os.ReadFile(global)
	if string(before) != string(after) || string(globalBefore) != string(globalAfter) {
		t.Fatal("refused install modified published config")
	}
}
