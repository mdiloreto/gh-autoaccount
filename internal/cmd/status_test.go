package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
)

func TestStatusJSONDirectorySelection(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	parent, nested := filepath.Join(home, "parent"), filepath.Join(home, "parent", "nested")
	child := filepath.Join(nested, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{
		{Dir: parent, User: "parent"},
		{Dir: nested, User: "nested", GitName: "Nested Name", GitEmail: "nested@example.test", SSHKey: "/fixture/key"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(parent)
	for _, tc := range []struct {
		args              []string
		dir, user, pinDir string
	}{
		{[]string{"status", "--json"}, parent, "parent", parent},
		{[]string{"status", "--json", "--dir", "nested/child"}, child, "nested", nested},
	} {
		stdout, stderr, err := executeDiagnostics(tc.args...)
		if err != nil || stderr != "" {
			t.Fatalf("status failed: %v, %s", err, stderr)
		}
		var status statusDiagnostics
		decodeDiagnosticJSON(t, stdout, &status)
		if status.SchemaVersion != 1 || status.Directory != tc.dir || status.Pin == nil || status.Pin.User != tc.user || status.Pin.Directory != tc.pinDir || status.Pin.Mode != config.ModeWrapper {
			t.Fatalf("wrong directory/pin selection: %s", stdout)
		}
		if status.Git.GitDir != "" || status.Git.AuthorIdent != "" || status.Git.CommitterIdent != "" || status.Git.Error == nil || status.Git.Error.Code != "repository_unavailable" {
			t.Fatalf("expected safe non-repository error: %+v", status.Git)
		}
		var raw map[string]any
		decodeDiagnosticJSON(t, stdout, &raw)
		wantEnvironment := map[string]any{"profile_user": "", "profile_mode": "", "profile_host": "", "gh_token_present": false, "github_token_present": false, "gh_enterprise_token_present": false, "github_enterprise_token_present": false}
		if !reflect.DeepEqual(raw["environment"], wantEnvironment) {
			t.Fatalf("unexpected environment schema: %+v", raw["environment"])
		}
		if tc.user == "nested" {
			wantPin := map[string]any{"directory": nested, "user": "nested", "host": "github.com", "token_command_configured": false, "mode": "wrapper", "git_name": "Nested Name", "git_email": "nested@example.test", "ssh_key": "/fixture/key"}
			if !reflect.DeepEqual(raw["pin"], wantPin) {
				t.Fatalf("status pin schema differs from list: %+v", raw["pin"])
			}
		}
	}
	calls, err := os.ReadFile(os.Getenv("DIAGNOSTICS_GH_LOG"))
	if err != nil || string(calls) != "auth token --user parent --hostname github.com\nauth token --user nested --hostname github.com\n" {
		t.Fatalf("JSON must use local auth token only: %q, %v", calls, err)
	}
}

func TestStatusTextReportsNativeRoutingWithoutDirenvLines(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	// An unmigrated export pin must not bring back direnv diagnostics.
	for _, mode := range []config.PinMode{config.ModeWrapper, config.ModeExport} {
		if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", Mode: mode}}}); err != nil {
			t.Fatal(err)
		}
		out, _, err := executeDiagnostics("status", "--dir", home)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Native routing selects this pin") || !strings.Contains(out, "shim PATH:            external, not verified") {
			t.Fatalf("mode %q: native routing not reported:\n%s", mode, out)
		}
		for _, absent := range []string{"routing mode", "direnv", "GH_AUTOPROFILE_", "token mode"} {
			if strings.Contains(out, absent) {
				t.Fatalf("mode %q: retired diagnostic %q present:\n%s", mode, absent, out)
			}
		}
		assertDiagnosticRedacted(t, out)
	}
}

func TestStatusAuthenticationPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, marker, markerHost, ghToken string
		mode                              config.PinMode
	}{
		{"wrapper ignores ambient", "alice", "github.com", diagnosticGHToken, config.ModeWrapper},
		{"export ignores ambient GH_TOKEN", "alice", "github.com", diagnosticGHToken, config.ModeExport},
		{"export ignores ambient GITHUB_TOKEN", "alice", "github.com", "", config.ModeExport},
		{"export mismatched marker", "other", "github.com", diagnosticGHToken, config.ModeExport},
		{"export partial user marker", "alice", "", diagnosticGHToken, config.ModeExport},
		{"export partial host marker", "", "github.com", diagnosticGHToken, config.ModeExport},
		{"export no marker", "", "", diagnosticGHToken, config.ModeExport},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := diagnosticsHome(t)
			forbidDiagnosticHTTP(t)
			t.Setenv("GH_AUTOPROFILE_USER", tc.marker)
			t.Setenv("GH_AUTOPROFILE_HOST", tc.markerHost)
			t.Setenv("GH_AUTOPROFILE_MODE", string(tc.mode))
			t.Setenv("GH_TOKEN", tc.ghToken)
			t.Setenv("GITHUB_TOKEN", diagnosticGitHubToken)
			if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", Mode: tc.mode}}}); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := executeDiagnostics("status", "--dir", home, "--json")
			if err != nil || stderr != "" {
				t.Fatalf("status failed: %v, %s", err, stderr)
			}
			assertDiagnosticRedacted(t, stdout+stderr)
			var status statusDiagnostics
			decodeDiagnosticJSON(t, stdout, &status)
			want := authenticationDiagnostics{Selected: true, Source: "keyring", CredentialAvailable: true}
			if !reflect.DeepEqual(status.Authentication, want) {
				t.Fatalf("wrong authentication policy: %+v", status.Authentication)
			}
			if status.Environment.ProfileUser != tc.marker || status.Environment.ProfileHost != tc.markerHost || status.Environment.ProfileMode != string(tc.mode) || status.Environment.GHTokenPresent != (tc.ghToken != "") || !status.Environment.GitHubTokenPresent {
				t.Fatalf("wrong environment presence: %+v", status.Environment)
			}
		})
	}
}

func TestStatusEnterpriseBrokerJSON(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	broker := filepath.Join(home, "private-broker")
	if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf 'broker-token\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", Host: "ENTERPRISE.EXAMPLE", TokenCommand: broker}}}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeDiagnostics("status", "--dir", home, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status statusDiagnostics
	decodeDiagnosticJSON(t, stdout, &status)
	if status.Pin == nil || status.Pin.Host != "enterprise.example" || !status.Pin.TokenCommandConfigured || status.Authentication.Source != "token_command" || !status.Authentication.CredentialAvailable || status.Authentication.Verified {
		t.Fatalf("unexpected status %s", stdout)
	}
	if strings.Contains(stdout, broker) || strings.Contains(stdout, "broker-token") {
		t.Fatalf("sensitive data in status: %s", stdout)
	}
}

func TestStatusUsesSelectedHostGitHelper(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", Host: "enterprise.example"}}}); err != nil {
		t.Fatal(err)
	}
	if err := gitcred.ConfigureHosts([]string{"github.com"}); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeDiagnostics("status", "--dir", home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "git helper active:    no") || strings.Contains(out, "Plain git HTTPS uses") {
		t.Fatalf("github.com helper reported active for Enterprise pin:\n%s", out)
	}
	if err := gitcred.ConfigureHosts([]string{"enterprise.example"}); err != nil {
		t.Fatal(err)
	}
	out, _, err = executeDiagnostics("status", "--dir", home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "git helper active:    yes") || !strings.Contains(out, "Plain git HTTPS uses") {
		t.Fatalf("Enterprise helper not reported active:\n%s", out)
	}
}

func TestStatusAmbientHostMarkerMismatch(t *testing.T) {
	home := diagnosticsHome(t)
	t.Setenv("GH_AUTOPROFILE_USER", "alice")
	t.Setenv("GH_AUTOPROFILE_HOST", "other.example")
	t.Setenv("GH_TOKEN", "ambient-token")
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice", Mode: config.ModeExport}}}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := executeDiagnostics("status", "--dir", home, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var status statusDiagnostics
	decodeDiagnosticJSON(t, stdout, &status)
	if status.Authentication.Source != "keyring" {
		t.Fatalf("stale host reused: %s", stdout)
	}
}

func TestStatusJSONUnpinnedHasNoRemoteIdentityClaim(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	t.Setenv("GH_TOKEN", diagnosticGHToken)
	t.Setenv("GITHUB_TOKEN", diagnosticGitHubToken)
	t.Setenv("GH_AUTOPROFILE_USER", "stale-account")
	stdout, stderr, err := executeDiagnostics("status", "--dir", home, "--json")
	if err != nil || stderr != "" {
		t.Fatalf("status failed: %v, %s", err, stderr)
	}
	assertDiagnosticRedacted(t, stdout)
	var status statusDiagnostics
	decodeDiagnosticJSON(t, stdout, &status)
	if status.Pin != nil || status.Authentication != (authenticationDiagnostics{Source: "default"}) {
		t.Fatalf("unpinned status asserted authentication: %+v", status)
	}
	if !strings.Contains(stdout, `"pin":null`) {
		t.Fatal("unpinned JSON must retain a nullable pin")
	}
	if _, err := os.Stat(os.Getenv("DIAGNOSTICS_GH_LOG")); !os.IsNotExist(err) {
		t.Fatalf("unpinned JSON invoked gh: %v", err)
	}
}

func TestStatusJSONCredentialFailureIsSafe(t *testing.T) {
	for _, failure := range []string{"fail", "empty", "invalid"} {
		t.Run(failure, func(t *testing.T) {
			home := diagnosticsHome(t)
			forbidDiagnosticHTTP(t)
			t.Setenv("DIAGNOSTICS_TOKEN_CASE", failure)
			if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice"}}}); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := executeDiagnostics("status", "--json", "--dir", home)
			if err != nil || stderr != "" {
				t.Fatalf("status should retain partial diagnostics: %v, %s", err, stderr)
			}
			assertDiagnosticRedacted(t, stdout)
			var status statusDiagnostics
			decodeDiagnosticJSON(t, stdout, &status)
			auth := status.Authentication
			if status.Pin == nil || !auth.Selected || auth.CredentialAvailable || auth.Verified || auth.Source != "unavailable" || auth.Error == nil || auth.Error.Code != "credential_unavailable" {
				t.Fatalf("wrong safe authentication error: %+v", status)
			}
			if !strings.Contains(auth.Error.Message, "gh auth login") {
				t.Fatal("credential failure lost recovery guidance")
			}
		})
	}
}

func TestStatusFatalErrorsAreSafe(t *testing.T) {
	for _, failure := range []string{"pins", "settings", "directory"} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(failure+map[bool]string{true: "/json", false: "/text"}[jsonOutput], func(t *testing.T) {
				home := diagnosticsHome(t)
				if err := config.SavePins(&config.PinRegistry{}); err != nil {
					t.Fatal(err)
				}
				dir := home
				if failure == "directory" {
					dir = filepath.Join(home, "missing")
				} else {
					configDir, err := config.ConfigDir()
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(configDir, failure+".yml"), []byte(diagnosticStoredToken+": invalid\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"status", "--dir", dir}
				if jsonOutput {
					args = append(args, "--json")
				}
				stdout, stderr, err := executeDiagnostics(args...)
				if err == nil || stdout != "" || (jsonOutput && stderr != "") {
					t.Fatalf("fatal error emitted output: %q, %q, %v", stdout, stderr, err)
				}
				assertDiagnosticRedacted(t, err.Error()+stderr)
				if failure != "directory" && !strings.Contains(err.Error(), failure+".yml") {
					t.Fatalf("error lost the affected config filename: %v", err)
				}
			})
		}
	}
}

func TestStatusHumanIsLocalAndRedacted(t *testing.T) {
	for _, failure := range []string{"good", "fail"} {
		t.Run(failure, func(t *testing.T) {
			home := diagnosticsHome(t)
			forbidDiagnosticHTTP(t)
			t.Setenv("GH_TOKEN", diagnosticGHToken)
			t.Setenv("GITHUB_TOKEN", diagnosticGitHubToken)
			t.Setenv("GIT_SSH_COMMAND", "ssh -o ProxyCommand="+diagnosticStoredToken)
			t.Setenv("GIT_CONFIG_COUNT", diagnosticGitHubToken)
			t.Setenv("DIAGNOSTICS_TOKEN_CASE", failure)
			if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice"}}}); err != nil {
				t.Fatal(err)
			}
			stdout, stderr, err := executeDiagnostics("status", "--dir", home)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnosticRedacted(t, stdout+stderr)
			for _, want := range []string{"Directory: " + home, "Pinned account:   alice", "GH_TOKEN:             (present)", "GITHUB_TOKEN:         (present)", "GitHub identity:   not verified", "Token scopes:      not inspected (local diagnostics only)"} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("missing %q in text: %s", want, stdout)
				}
			}
			calls, err := os.ReadFile(os.Getenv("DIAGNOSTICS_GH_LOG"))
			if err != nil || string(calls) != "auth token --user alice --hostname github.com\n" {
				t.Fatalf("human status must only look up a local token: %q, %v", calls, err)
			}
		})
	}
}

func TestStatusHumanUnpinnedAbsenceDoesNotClaimLogin(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	stdout, stderr, err := executeDiagnostics("status", "--dir", home)
	if err != nil {
		t.Fatal(err)
	}
	assertDiagnosticRedacted(t, stdout+stderr)
	for _, want := range []string{"GH_TOKEN:             (not set)", "GITHUB_TOKEN:         (not set)", "Selected credential: (none; default gh behavior)", "No pin selected. Default gh behavior; GitHub identity is not verified."} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in text: %s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Using default gh account") {
		t.Fatal("unpinned status claimed an effective login without verification")
	}
	if _, err := os.Stat(os.Getenv("DIAGNOSTICS_GH_LOG")); !os.IsNotExist(err) {
		t.Fatalf("unpinned human status invoked gh: %v", err)
	}
}

func TestStatusHumanTerminalControlsAndJSONPreservation(t *testing.T) {
	home := diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	controls := "\x1b]52;c;fixture\a\x7f\u0085\u009b"
	pinDir := filepath.Join(home, "raí́z"+controls)
	repo := filepath.Join(pinDir, "répo")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	diagnosticGit(t, repo, "init", "--quiet")
	author, committer := "Áutora"+controls, "提交者"+controls
	t.Setenv("GIT_AUTHOR_NAME", author)
	t.Setenv("GIT_AUTHOR_EMAIL", "author@example.test")
	t.Setenv("GIT_COMMITTER_NAME", committer)
	t.Setenv("GIT_COMMITTER_EMAIL", "committer@example.test")
	t.Setenv("GH_AUTOPROFILE_USER", "alíce"+controls)
	t.Setenv("GH_AUTOPROFILE_MODE", "wrapper"+controls)
	t.Setenv("DIAGNOSTICS_TOKEN_CASE", "fail")
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{
		Dir: pinDir, User: "alíce" + controls, GitName: "Nómbre" + controls,
		GitEmail: "pinned@example.test" + controls, SSHKey: filepath.Join(pinDir, "clé"),
	}}}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeDiagnostics("status", "--dir", repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range stdout + stderr {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Errorf("human status emitted terminal control U+%04X", r)
		}
	}
	for _, want := range []string{"raí́z", "répo", "Áutora", "提交者", "alíce", "Nómbre", "clé"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human status lost Unicode value %q", want)
		}
	}
	jsonOutput, jsonErrors, err := executeDiagnostics("status", "--dir", repo, "--json")
	if err != nil || jsonErrors != "" {
		t.Fatalf("JSON status failed: %v, %s", err, jsonErrors)
	}
	var status statusDiagnostics
	decodeDiagnosticJSON(t, jsonOutput, &status)
	if status.Directory != repo || status.Pin == nil || status.Pin.Directory != pinDir || status.Pin.User != "alíce"+controls || status.Environment.ProfileMode != "wrapper"+controls {
		t.Fatal("JSON values or raw pin lookup were altered by human sanitization")
	}
	if status.Git.GitDir != filepath.Join(repo, ".git") || !strings.HasPrefix(status.Git.AuthorIdent, author+" <") || !strings.HasPrefix(status.Git.CommitterIdent, committer+" <") {
		t.Fatalf("JSON did not retain actual Git identities: %+v", status.Git)
	}
	if strings.ContainsRune(jsonOutput, '\x1b') || !strings.Contains(jsonOutput, `\u001b`) {
		t.Fatal("JSON must escape ESC while preserving its original decoded value")
	}
}

func TestStatusSanitizeValue(t *testing.T) {
	var controls strings.Builder
	for r := rune(0); r < 0x20; r++ {
		controls.WriteRune(r)
	}
	for r := rune(0x7f); r <= 0x9f; r++ {
		controls.WriteRune(r)
	}
	want := "Árbol 中文 e\u0301 😀\u00a0"
	if got := sanitizeStatusValue(controls.String() + want + controls.String()); got != want {
		t.Fatalf("expected all C0/DEL/C1 removed and Unicode preserved, got %q", got)
	}
}
