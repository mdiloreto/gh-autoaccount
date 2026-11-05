package gitcred

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestManagedHelperActiveFollowsGitsHelperList checks the list Git itself builds
// from every config level: an empty value anywhere clears what came before.
func TestManagedHelperActiveFollowsGitsHelperList(t *testing.T) {
	for _, tc := range []struct {
		name      string
		local     [][2]string // repository-local key/value pairs, in order
		wantOn    bool
		wantReset bool // the clearing entry is the repository's own config
	}{
		{name: "global chain applies", wantOn: true},
		{name: "repository resets the host helper", local: [][2]string{{"credential.https://github.com.helper", ""}, {"credential.https://github.com.helper", "!gh auth git-credential"}}, wantReset: true},
		{name: "repository resets the generic helper", local: [][2]string{{"credential.helper", ""}}, wantReset: true},
		{name: "managed helper added after a reset", local: [][2]string{{"credential.https://github.com.helper", ""}, {"credential.https://github.com.helper", "MANAGED"}}, wantOn: true},
		{name: "trailing slash in the URL still matches", local: [][2]string{{"credential.https://github.com/.helper", ""}}, wantReset: true},
		{name: "another host's reset does not apply", local: [][2]string{{"credential.https://ghe.example.test.helper", ""}}, wantOn: true},
		{name: "extra helper after ours keeps it active", local: [][2]string{{"credential.helper", "store"}}, wantOn: true},
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
			active, resetBy, err := ManagedHelperActive(repo, "github.com")
			if err != nil {
				t.Fatal(err)
			}
			if active != tc.wantOn {
				t.Fatalf("active = %v, want %v", active, tc.wantOn)
			}
			if gotLocal := strings.HasSuffix(resetBy, filepath.Join(".git", "config")); !tc.wantOn && gotLocal != tc.wantReset {
				t.Fatalf("resetBy = %q, want the repository config: %v", resetBy, tc.wantReset)
			}
		})
	}
}

func TestEffectiveHelpersWithNoConfiguration(t *testing.T) {
	isolatedGlobalGit(t)
	chain, resetBy, err := EffectiveHelpers(t.TempDir(), "github.com")
	if err != nil || len(chain) != 0 || resetBy != "" {
		t.Fatalf("chain=%v resetBy=%q err=%v", chain, resetBy, err)
	}
}

func git(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
