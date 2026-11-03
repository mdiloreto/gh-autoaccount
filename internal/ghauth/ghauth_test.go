package ghauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAuthStatus(t *testing.T) {
	// Simulated gh auth status output
	output := `github.com
  ✓ Logged in to github.com account alice (keyring)
  - Active account: true
  - Git operations protocol: https
  - Token: gho_************************************

  ✓ Logged in to github.com account bob-work (keyring)
  - Active account: false
  - Git operations protocol: https
  - Token: gho_************************************

  ✓ Logged in to github.com account alice-freelance (keyring)
  - Active account: false
  - Git operations protocol: https
  - Token: gho_************************************
`

	users := parseAuthStatus(output)

	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}

	// First user
	if users[0].User != "alice" {
		t.Errorf("user 0: expected 'alice', got '%s'", users[0].User)
	}
	if !users[0].Active {
		t.Error("user 0: expected active")
	}
	if users[0].Host != "github.com" {
		t.Errorf("user 0: expected host 'github.com', got '%s'", users[0].Host)
	}
	if users[0].Protocol != "https" {
		t.Errorf("user 0: expected protocol 'https', got '%s'", users[0].Protocol)
	}

	// Second user
	if users[1].User != "bob-work" {
		t.Errorf("user 1: expected 'bob-work', got '%s'", users[1].User)
	}
	if users[1].Active {
		t.Error("user 1: expected inactive")
	}

	// Third user
	if users[2].User != "alice-freelance" {
		t.Errorf("user 2: expected 'alice-freelance', got '%s'", users[2].User)
	}
	if users[2].Active {
		t.Error("user 2: expected inactive")
	}
}

func TestParseAuthStatus_Empty(t *testing.T) {
	users := parseAuthStatus("")
	if len(users) != 0 {
		t.Errorf("expected 0 users for empty output, got %d", len(users))
	}
}

func TestSanitizeTerminal(t *testing.T) {
	// The sanitizer must operate on decoded runes, not bytes: valid multibyte
	// UTF-8 survives, true C1 controls (U+0080–U+009F) are stripped, and
	// invalid bytes degrade to the harmless replacement character.
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"printable ascii preserved", "alice-work_1.@", "alice-work_1.@"},
		{"multibyte utf8 preserved", "álïcé-工作", "álïcé-工作"},
		{"ESC stripped (kills CSI/OSC introducers)", "a\x1b]52;c;pbwnb\x07b", "a]52;c;pbwnbb"},
		{"BEL stripped", "a\x07b", "ab"},
		{"NUL and newline stripped", "a\x00\nb", "ab"},
		{"DEL stripped", "a\x7fb", "ab"},
		{"true C1 control (U+0085 NEL) stripped", "a\u0085b", "ab"},
		{"C1 upper boundary stripped (U+009F)", "a\u009fb", "ab"},
		{"just above C1 (U+00A0 NBSP) preserved", "a\u00a0b", "a\u00a0b"},
		{"all controls removed", "\x1b\x07\x00\x1f\x7f", ""},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeTerminal(tt.input); got != tt.want {
				t.Errorf("sanitizeTerminal(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseAuthStatus_SanitizesTerminalSequences(t *testing.T) {
	// A hijacked gh must not be able to inject terminal control sequences
	// (OSC 52 clipboard writes, CSI cursor/screen control) into account data
	// that setup/status/doctor re-emit.
	output := "github.com\n" +
		"  * Logged in to github.com account evil-\x1b]52;c;aZWNobyBwYXduZWQ=\x07\x1b]2;PWNED\x07name (keyring)\n" +
		"    - Active account: true\n" +
		"    - Git operations protocol: ht\x1b[2Jtps\n" +
		"    - Token scopes: 'repo', '\x1bAwork'\n"

	users := parseAuthStatus(output)
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"user", users[0].User},
		{"host", users[0].Host},
		{"protocol", users[0].Protocol},
	} {
		if strings.ContainsAny(tc.got, "\x1b\x07") {
			t.Errorf("%s contains terminal control bytes: %q", tc.name, tc.got)
		}
	}
	for i, scope := range users[0].Scopes {
		if strings.ContainsAny(scope, "\x1b\x07") {
			t.Errorf("scope %d contains terminal control bytes: %q", i, scope)
		}
	}
	if !strings.Contains(users[0].User, "evil-") || !strings.Contains(users[0].User, "name") {
		t.Errorf("sanitization should preserve printable characters, got %q", users[0].User)
	}
}

func TestParseAuthStatus_SingleUser(t *testing.T) {
	output := `github.com
  ✓ Logged in to github.com account alice (keyring)
  - Active account: true
  - Git operations protocol: ssh
  - Token: gho_****
`
	users := parseAuthStatus(output)
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].User != "alice" {
		t.Errorf("expected 'alice', got '%s'", users[0].User)
	}
	if users[0].Protocol != "ssh" {
		t.Errorf("expected protocol 'ssh', got '%s'", users[0].Protocol)
	}
}

func TestParseAuthStatus_ScopesAndInvalidEntries(t *testing.T) {
	output := `github.com
  ✓ Logged in to github.com account alice (GH_TOKEN)
  - Active account: true
  - Git operations protocol: https
  - Token scopes: 'gist', 'read:org', 'repo'

  ✓ Logged in to github.com account bob (keyring)
  - Active account: false
  - Git operations protocol: https
  - Token scopes: 'gist', 'read:org', 'repo', 'workflow'

  X Failed to log in to github.com account carol (default)
  - Active account: false
  - The token in default is invalid.
`

	users := parseAuthStatus(output)
	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}
	if !users[0].Valid {
		t.Fatalf("expected first user to be valid: %+v", users[0])
	}
	if got := strings.Join(users[1].Scopes, ","); got != "gist,read:org,repo,workflow" {
		t.Fatalf("unexpected parsed scopes: %q", got)
	}
	if users[2].Valid {
		t.Fatalf("expected third user to be invalid: %+v", users[2])
	}
}

// --- GetTokenWithFallback tests ---

func TestGetTokenWithFallback_GH_TOKEN(t *testing.T) {
	// GH_TOKEN should take highest priority.
	t.Setenv("GH_TOKEN", "tok-from-ghtoken")
	t.Setenv("GITHUB_TOKEN", "tok-from-githubtoken")

	token, source, err := GetTokenWithFallback("nonexistent-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-from-ghtoken" {
		t.Errorf("expected GH_TOKEN value, got %q", token)
	}
	if source != "GH_TOKEN" {
		t.Errorf("expected source 'GH_TOKEN', got %q", source)
	}
}

func TestGetTokenWithFallback_GITHUB_TOKEN(t *testing.T) {
	// When GH_TOKEN is unset, GITHUB_TOKEN should be used.
	os.Unsetenv("GH_TOKEN")
	t.Setenv("GITHUB_TOKEN", "tok-from-githubtoken")

	token, source, err := GetTokenWithFallback("nonexistent-user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "tok-from-githubtoken" {
		t.Errorf("expected GITHUB_TOKEN value, got %q", token)
	}
	if source != "GITHUB_TOKEN" {
		t.Errorf("expected source 'GITHUB_TOKEN', got %q", source)
	}
}

func TestGetTokenWithFallback_KeyringFallback(t *testing.T) {
	// When no env vars are set, it falls back to `gh auth token`.
	// In CI/test this will likely fail — that's expected.
	os.Unsetenv("GH_TOKEN")
	os.Unsetenv("GITHUB_TOKEN")

	_, _, err := GetTokenWithFallback("definitely-not-a-real-user-xyzzy")
	if err == nil {
		t.Error("expected error for non-existent user with no env vars")
	}
}

func TestGetTokenWithFallback_EmptyEnvVarsIgnored(t *testing.T) {
	// Empty env vars should be treated the same as unset.
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	_, _, err := GetTokenWithFallback("definitely-not-a-real-user-xyzzy")
	if err == nil {
		t.Error("expected error: empty env vars should not count as tokens")
	}
}

func TestIsInteractive_InTests(t *testing.T) {
	// In test/CI, stdin is typically not a TTY.
	// We can't assert a specific value portably, but we can verify
	// the function doesn't panic.
	_ = IsInteractive()
}

func TestNonInteractiveWarning_WithToken(t *testing.T) {
	// If a token is available via env var, no warning should be returned
	// even in non-interactive mode.
	t.Setenv("GH_TOKEN", "tok-test")
	warning := NonInteractiveWarning("some-user")
	if warning != "" {
		// It should be empty if interactive OR if token resolves.
		// Since we set GH_TOKEN, it should always resolve.
		t.Errorf("expected empty warning when token is available, got: %s", warning)
	}
}

func TestValidateUser_RequiresNamedStoredToken(t *testing.T) {
	gh := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}" || exit 91
test "$5 $6" = '--hostname github.com' || exit 92
case "$4" in
  alice) printf 'stored-alice\n' ;;
  empty) exit 0 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	t.Setenv("GH_TOKEN", "unrelated-token")
	t.Setenv("GITHUB_TOKEN", "other-token")
	for _, user := range []string{"alice", "empty", "nonexistent-user"} {
		err := ValidateUser(user)
		if (err == nil) != (user == "alice") {
			t.Errorf("ValidateUser(%q) = %v", user, err)
		}
	}
}
