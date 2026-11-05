package gitcred

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedHelperFollowsGitsHelperList checks the list Git itself builds from
// every config level: an empty value anywhere clears what came before, and URL
// sections match the way Git matches them.
func TestManagedHelperFollowsGitsHelperList(t *testing.T) {
	const host = "credential.https://github.com.helper"
	for _, tc := range []struct {
		name       string
		local      [][2]string // repository-local key/value pairs, in order
		wantOn     bool
		wantReset  bool   // the clearing entry is the repository's own config
		wantAfter  string // the helper Git asks before ours
		wantScoped int    // repository-path sections reported
	}{
		{name: "global chain applies", wantOn: true},
		{name: "repository resets the host helper", local: [][2]string{{host, ""}, {host, "!gh auth git-credential"}}, wantReset: true},
		{name: "repository resets the generic helper", local: [][2]string{{"credential.helper", ""}}, wantReset: true},
		{name: "managed helper added after a reset", local: [][2]string{{host, ""}, {host, "MANAGED"}}, wantOn: true},
		{name: "trailing slash matches", local: [][2]string{{"credential.https://github.com/.helper", ""}}, wantReset: true},
		{name: "dot path matches", local: [][2]string{{"credential.https://github.com/./.helper", ""}}, wantReset: true},
		{name: "default port matches", local: [][2]string{{"credential.https://github.com:443.helper", ""}}, wantReset: true},
		{name: "wildcard host matches", local: [][2]string{{"credential.https://*.com.helper", ""}}, wantReset: true},
		{name: "context without a scheme matches", local: [][2]string{{"credential.github.com.helper", ""}}, wantReset: true},
		{name: "other port does not match", local: [][2]string{{"credential.https://github.com:8443.helper", ""}}, wantOn: true},
		{name: "another host's reset does not apply", local: [][2]string{{"credential.https://ghe.example.test.helper", ""}}, wantOn: true},
		{name: "repository path section is reported, not applied", local: [][2]string{{"credential.https://github.com/org.helper", ""}}, wantOn: true, wantScoped: 1},
		{name: "a helper before ours is reported", local: [][2]string{{host, ""}, {host, "store"}, {host, "MANAGED"}}, wantOn: true, wantAfter: "store"},
		{name: "extra helper after ours keeps it first", local: [][2]string{{"credential.helper", "store"}}, wantOn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolatedGlobalGit(t)
			if err := ConfigureGit(); err != nil {
				t.Fatal(err)
			}
			helperPath, err := HelperPath()
			if err != nil {
				t.Fatal(err)
			}
			repo := t.TempDir()
			git(t, "init", "-q", repo)
			for _, kv := range tc.local {
				value := strings.ReplaceAll(kv[1], "MANAGED", primaryHelper(helperPath))
				git(t, "-C", repo, "config", "--local", "--add", kv[0], value)
			}
			state, err := ManagedHelper(repo, "github.com")
			if err != nil {
				t.Fatal(err)
			}
			if state.Active != tc.wantOn || state.RunsAfter != tc.wantAfter || len(state.PathScoped) != tc.wantScoped {
				t.Fatalf("state = %+v, want active=%v after=%q scoped=%d", state, tc.wantOn, tc.wantAfter, tc.wantScoped)
			}
			if gotLocal := strings.HasSuffix(state.ResetBy, filepath.Join(".git", "config")); !tc.wantOn && gotLocal != tc.wantReset {
				t.Fatalf("ResetBy = %q, want the repository config: %v", state.ResetBy, tc.wantReset)
			}
		})
	}
}

// A key written without "=" is not an empty reset: Git refuses the config.
func TestManagedHelperRejectsValuelessHelperKey(t *testing.T) {
	isolatedGlobalGit(t)
	repo := t.TempDir()
	git(t, "init", "-q", repo)
	f, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("[credential]\n\thelper\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ManagedHelper(repo, "github.com"); err == nil || !strings.Contains(err.Error(), "without a value") {
		t.Fatalf("want a refused-config error, got %v", err)
	}
}

func TestEffectiveHelpersWithNoConfiguration(t *testing.T) {
	isolatedGlobalGit(t)
	chain, resetBy, scoped, err := EffectiveHelpers(t.TempDir(), "github.com")
	if err != nil || len(chain) != 0 || resetBy != "" || len(scoped) != 0 {
		t.Fatalf("chain=%v resetBy=%q scoped=%v err=%v", chain, resetBy, scoped, err)
	}
}

func git(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
