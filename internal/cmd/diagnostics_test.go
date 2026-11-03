package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
)

const (
	diagnosticStoredToken = "STOR-synthetic-credential-ZEND"
	diagnosticGHToken     = "AMBI-synthetic-gh-credential-GHZZ"
	diagnosticGitHubToken = "OTHR-synthetic-github-credential-HUBZ"
)

func TestStatusDefaultSelectionAndRecovery(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice", Native: true}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIAGNOSTICS_TOKEN_CASE", "fail")
	stdout, stderr, err := executeDiagnostics("status", "--dir", home, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("status: %q %q %v", stdout, stderr, err)
	}
	var d statusDiagnostics
	decodeDiagnosticJSON(t, stdout, &d)
	if d.SchemaVersion != 1 || d.Pin != nil || d.SelectedUser != "alice" || d.SelectionSource != "default" || !d.Authentication.Selected || d.Authentication.CredentialAvailable || d.Authentication.Error == nil || d.Authentication.Error.Code != "credential_unavailable" {
		t.Fatalf("unavailable default: %+v", d)
	}
	assertDiagnosticRedacted(t, stdout)
	if _, _, err := executeDiagnostics("default", "--unset"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	stdout, stderr, err = executeDiagnostics("status", "--dir", home, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("status after unset: %q %q %v", stdout, stderr, err)
	}
	decodeDiagnosticJSON(t, stdout, &d)
	if d.SelectedUser != "" || d.SelectionSource != "none" || d.Authentication.Selected {
		t.Fatalf("unset selection: %+v", d)
	}
}

func diagnosticsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GH_CONFIG_DIR", filepath.Join(home, "gh-config"))
	t.Setenv("DIRENV_CONFIG", filepath.Join(home, "direnv"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	for _, key := range []string{
		"GH_TOKEN", "GITHUB_TOKEN", "GH_AUTOPROFILE_USER", "GH_AUTOPROFILE_MODE", "GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN",
		"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GIT_AUTHOR_DATE", "2000-01-01T00:00:00+0000")
	t.Setenv("GIT_COMMITTER_DATE", "2000-01-01T00:00:00+0000")
	t.Setenv("DIAGNOSTICS_STORED_TOKEN", diagnosticStoredToken)
	t.Setenv("DIAGNOSTICS_TOKEN_CASE", "good")
	t.Setenv("DIAGNOSTICS_GH_LOG", filepath.Join(home, "gh-calls"))
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	gh := filepath.Join(bin, "gh")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$DIAGNOSTICS_GH_LOG"
case "$1 $2" in
  'auth token')
    test -z "$GH_TOKEN$GITHUB_TOKEN" || exit 91
    test "$3 $5 $6" = '--user --hostname github.com' || exit 92
    case "$DIAGNOSTICS_TOKEN_CASE" in
      fail) printf '%s\n' "$DIAGNOSTICS_STORED_TOKEN"; printf '%s\n' "$DIAGNOSTICS_STORED_TOKEN" >&2; exit 1 ;;
      empty) exit 0 ;;
      invalid) printf '%s\npassword=invalid\n' "$DIAGNOSTICS_STORED_TOKEN" ;;
      *) printf '%s\n' "$DIAGNOSTICS_STORED_TOKEN" ;;
    esac ;;
  *) printf '%s\n' "$DIAGNOSTICS_STORED_TOKEN" >&2; exit 94 ;;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home
}

func executeDiagnostics(args ...string) (string, string, error) {
	cmd := NewRootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func decodeDiagnosticJSON(t *testing.T, output string, target any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("invalid diagnostic JSON: %v\n%s", err, output)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("expected exactly one JSON document, got %v", err)
	}
}

func assertDiagnosticRedacted(t *testing.T, output string) {
	t.Helper()
	for _, token := range []string{diagnosticStoredToken, diagnosticGHToken, diagnosticGitHubToken} {
		for _, forbidden := range []string{token, token[:4], token[len(token)-4:]} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("diagnostics exposed a synthetic token or fragment: %q", output)
			}
		}
	}
}

type diagnosticTransport func(*http.Request) (*http.Response, error)

func (f diagnosticTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func forbidDiagnosticHTTP(t *testing.T) {
	t.Helper()
	previous := http.DefaultTransport
	calls := 0
	http.DefaultTransport = diagnosticTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("HTTP forbidden in local diagnostics")
	})
	t.Cleanup(func() {
		http.DefaultTransport = previous
		if calls != 0 {
			t.Errorf("local diagnostics attempted %d HTTP requests", calls)
		}
	})
}

func diagnosticGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDiagnosticGitIdentities(t *testing.T) {
	home := diagnosticsHome(t)
	diagnosticGit(t, home, "init", "--quiet")
	diagnosticGit(t, home, "config", "user.name", "Local User")
	diagnosticGit(t, home, "config", "user.email", "local@example.test")
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprintf("managed=%t", managed), func(t *testing.T) {
			if err := config.SaveSettings(config.Settings{GitIdentity: managed}); err != nil {
				t.Fatal(err)
			}
			if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{
				Dir: home, User: "alice", GitName: "Pinned User", GitEmail: "pinned@example.test",
			}}}); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := executeDiagnostics("status", "--dir", home, "--json")
			if err != nil || stderr != "" {
				t.Fatalf("status failed: %v, %s", err, stderr)
			}
			var status statusDiagnostics
			decodeDiagnosticJSON(t, stdout, &status)
			if status.Git.Error != nil || status.Git.GitDir != filepath.Join(home, ".git") {
				t.Fatalf("unexpected Git diagnostics: %+v", status.Git)
			}
			if !strings.HasPrefix(status.Git.AuthorIdent, "Local User <local@example.test>") || !strings.HasPrefix(status.Git.CommitterIdent, "Local User <local@example.test>") {
				t.Fatalf("pin metadata replaced real Git identities: %+v", status.Git)
			}
			t.Setenv("GIT_AUTHOR_NAME", "Author Override")
			t.Setenv("GIT_AUTHOR_EMAIL", "author@example.test")
			t.Setenv("GIT_COMMITTER_NAME", "Committer Override")
			t.Setenv("GIT_COMMITTER_EMAIL", "committer@example.test")
			actual := inspectGit(home)
			if actual.Error != nil || !strings.HasPrefix(actual.AuthorIdent, "Author Override <author@example.test>") || !strings.HasPrefix(actual.CommitterIdent, "Committer Override <committer@example.test>") {
				t.Fatalf("environment overrides were ignored: %+v", actual)
			}
		})
	}
}

func TestDiagnosticGitWorktreeSeparatesAuthenticationPin(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	checkoutRoot, identityRoot := filepath.Join(home, "checkout-root"), filepath.Join(home, "identity-root")
	for _, dir := range []string{checkoutRoot, identityRoot} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	repo, worktree := filepath.Join(identityRoot, "repo"), filepath.Join(checkoutRoot, "worktree")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	diagnosticGit(t, repo, "init", "--quiet")
	registry := &config.PinRegistry{Pins: []config.Pin{
		{Dir: checkoutRoot, User: "checkout-user", GitName: "Checkout Identity", GitEmail: "checkout@example.test"},
		{Dir: identityRoot, User: "gitdir-user", GitName: "Gitdir Identity", GitEmail: "gitdir@example.test"},
	}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	if err := gitidentity.Install(registry); err != nil {
		t.Fatal(err)
	}
	diagnosticGit(t, repo, "-c", "core.hooksPath="+os.DevNull, "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "fixture")
	diagnosticGit(t, repo, "worktree", "add", "--quiet", "-b", "diagnostic-fixture", worktree)
	stdout, stderr, err := executeDiagnostics("status", "--json", "--dir", worktree)
	if err != nil || stderr != "" {
		t.Fatalf("status failed: %v, %s", err, stderr)
	}
	var status statusDiagnostics
	decodeDiagnosticJSON(t, stdout, &status)
	if status.Pin == nil || status.Pin.User != "checkout-user" || !status.Authentication.Selected || status.Authentication.Verified {
		t.Fatalf("incorrect checkout authentication selection: %+v", status)
	}
	if status.Git.GitDir != diagnosticGit(t, worktree, "rev-parse", "--absolute-git-dir") || !strings.HasPrefix(status.Git.GitDir, identityRoot+string(os.PathSeparator)) {
		t.Fatalf("incorrect worktree gitdir: %+v", status.Git)
	}
	for _, ident := range []string{status.Git.AuthorIdent, status.Git.CommitterIdent} {
		if !strings.HasPrefix(ident, "Gitdir Identity <gitdir@example.test>") || strings.Contains(ident, "checkout@example.test") {
			t.Fatalf("Git identity incorrectly follows checkout pin: %s", ident)
		}
	}
}

func TestDiagnosticGitIdentityFailureIsPartial(t *testing.T) {
	home := diagnosticsHome(t)
	diagnosticGit(t, home, "init", "--quiet")
	diagnosticGit(t, home, "config", "user.useConfigOnly", "true")
	actual := inspectGit(home)
	if actual.GitDir == "" || actual.AuthorIdent != "" || actual.CommitterIdent != "" || actual.Error == nil || actual.Error.Code != "identity_unavailable" {
		t.Fatalf("expected repository information with safe identity error: %+v", actual)
	}
}

func TestDiagnosticGitPreservesGitDirWhitespace(t *testing.T) {
	home := diagnosticsHome(t)
	repo := filepath.Join(home, "bare repository ")
	diagnosticGit(t, home, "init", "--quiet", "--bare", repo)
	actual := inspectGit(repo)
	if actual.GitDir != repo {
		t.Fatalf("Git directory whitespace was lost: got %q, want %q", actual.GitDir, repo)
	}
}
