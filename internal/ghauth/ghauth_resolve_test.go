package ghauth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

func TestGHPathIsAuthoritative(t *testing.T) {
	root := t.TempDir()
	trusted := filepath.Join(root, "trusted")
	marker := filepath.Join(root, "marker")
	attackerDir := filepath.Join(root, "path")
	if err := os.Mkdir(attackerDir, 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(root, "log")
	trustedScript := "#!/bin/sh\nprintf 'trusted\\n' >> \"$TEST_GH_PATH_LOG\"\ncase \"$1\" in auth) printf 'stored-token\\n';; api) printf 'alice\\n';; *) exit 2;; esac\n"
	attackScript := "#!/bin/sh\nprintf 'attacker\\n' >> \"$TEST_GH_PATH_LOG\"\nexit 1\n"
	for path, script := range map[string]string{trusted: trustedScript, marker: attackScript, filepath.Join(attackerDir, "gh"): attackScript} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GH_PATH", trusted)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", marker)
	t.Setenv("PATH", attackerDir)
	t.Setenv("TEST_GH_PATH_LOG", log)
	path, err := ResolveGHPath()
	if err != nil || path != trusted {
		t.Fatalf("path %q: %v", path, err)
	}
	token, err := ResolveToken(context.Background(), TokenRequest{User: "alice"})
	if err != nil || token != "stored-token" {
		t.Fatalf("resolve: %v", err)
	}
	if err := VerifyTokenIdentity(context.Background(), "alice", "github.com", token); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "trusted\ntrusted\n" {
		t.Fatalf("calls %q: %v", calls, err)
	}
}

func TestInvalidGHPathFailsClosed(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "gh")
	if err := os.WriteFile(marker, []byte("#!/bin/sh\nprintf 'stored-token\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(root, "shim")
	if err := os.WriteFile(shim, []byte("#!/usr/bin/env bash\n# gh-autoprofile-gh-shim\nset -euo pipefail\n"), 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", marker)
	t.Setenv("PATH", root)
	for _, path := range []string{"relative/gh", filepath.Join(root, "missing"), root, self, shim} {
		t.Run(path, func(t *testing.T) {
			t.Setenv("GH_PATH", path)
			if got, err := ResolveGHPath(); err == nil || got != "" {
				t.Fatalf("invalid GH_PATH fell back to %q: %v", got, err)
			}
			if _, err := ResolveToken(context.Background(), TokenRequest{User: "alice"}); err == nil {
				t.Fatal("token lookup fell back")
			}
			if err := VerifyTokenIdentity(context.Background(), "alice", "github.com", "valid-token"); err == nil {
				t.Fatal("identity verification fell back")
			}
		})
	}
	t.Setenv("GH_PATH", "")
	if got, err := ResolveGHPath(); err != nil || got != marker {
		t.Fatalf("no GH_PATH marker compatibility: %q %v", got, err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", shim)
	if _, err := ResolveGHPath(); err == nil {
		t.Fatal("shim marker accepted")
	}
	shimDir := filepath.Join(root, "shim-dir")
	if err := os.Mkdir(shimDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shim, filepath.Join(shimDir, "gh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+root)
	if got, err := ResolveGHPath(); err != nil || got != marker {
		t.Fatalf("PATH fallback did not skip shim: %q %v", got, err)
	}
}

func TestHostCredentialEnvironment(t *testing.T) {
	for _, tc := range []struct{ host, primary, fallback string }{{"github.com", "GH_TOKEN", "GITHUB_TOKEN"}, {"tenant.ghe.com", "GH_TOKEN", "GITHUB_TOKEN"}, {"ghe.com", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}, {"git.example.com", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}} {
		for _, both := range []bool{false, true} {
			env, err := CredentialEnv([]string{"GH_TOKEN=old", "GH_TOKEN=old2", "GITHUB_TOKEN=old", "GH_ENTERPRISE_TOKEN=old", "GITHUB_ENTERPRISE_TOKEN=old", "GH_HOST=old", "SAFE=yes"}, tc.host, "secret", both)
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(env, "\n")
			if strings.Contains(joined, "old") || !strings.Contains(joined, tc.primary+"=secret") || strings.Contains(joined, tc.fallback+"=secret") != both {
				t.Fatalf("bad env for %s: %s", tc.host, joined)
			}
			if strings.Contains(joined, "GH_HOST="+tc.host) != (tc.host != "github.com") {
				t.Fatal("wrong host env")
			}
		}
	}
}

func TestHostKeyringIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}${GH_ENTERPRISE_TOKEN+x}${GITHUB_ENTERPRISE_TOKEN+x}${GH_HOST+x}" || exit 1
test "$*" = 'auth token --user alice --hostname git.example.com' || exit 2
printf 'stored\n'
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", path)
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(key, "stale")
	}
	token, err := ResolveToken(context.Background(), TokenRequest{User: "alice", Host: "Git.Example.com"})
	if err != nil || token != "stored" {
		t.Fatalf("got %q: %v", token, err)
	}
}

func TestInvalidLoginRejectedBeforeCredentialLaunch(t *testing.T) {
	for _, user := range []string{"", "-alice", "alice-", "alice--bob", "alice bob", "alice\nother", "alice_", "alice_bob_extra", strings.Repeat("a", 40)} {
		if _, err := ResolveToken(context.Background(), TokenRequest{User: user, TokenCommand: "/does/not/exist"}); err == nil || err.Error() != "invalid user" {
			t.Fatalf("user %q: %v", user, err)
		}
	}
}

func TestValidLogin(t *testing.T) {
	for _, tc := range []struct {
		user  string
		valid bool
	}{
		{"a", true}, {"Alice-123", true}, {strings.Repeat("a", 39), true},
		{"mona-cat_octo", true}, {"2abvd19d_admin", true}, {"a_1", true},
		{strings.Repeat("a", 37) + "_1", true},
		{"", false}, {"-alice", false}, {"alice-", false}, {"alice--bob", false},
		{"_alice", false}, {"alice_", false}, {"alice_bob_extra", false},
		{"-alice_code", false}, {"alice-_code", false}, {"alice--bob_code", false},
		{"alice_co-de", false}, {"alice_!", false}, {"alice code", false},
		{"alice_é", false}, {"alice_\n", false}, {"alice_\x00", false},
		{strings.Repeat("a", 40), false}, {strings.Repeat("a", 38) + "_1", false},
	} {
		t.Run(tc.user, func(t *testing.T) {
			if got := validLogin(tc.user); got != tc.valid {
				t.Fatalf("validLogin(%q) = %v, want %v", tc.user, got, tc.valid)
			}
		})
	}
}

func TestManagedLoginBrokerAndIdentity(t *testing.T) {
	root := t.TempDir()
	broker := filepath.Join(root, "broker")
	gh := filepath.Join(root, "gh")
	brokerScript := "#!/bin/sh\ntest \"$GH_AUTOPROFILE_USER\" = mona-cat_octo || exit 1\ntest \"$GH_AUTOPROFILE_HOST\" = tenant.ghe.com || exit 2\nprintf 'selected\\n'\n"
	ghScript := "#!/bin/sh\ntest \"$*\" = 'api --hostname tenant.ghe.com user --jq .login' || exit 1\ntest \"$GH_TOKEN\" = selected || exit 2\nprintf 'MONA-CAT_OCTO\\n'\n"
	for path, script := range map[string]string{broker: brokerScript, gh: ghScript} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("GH_PATH", gh)
	token, err := ResolveToken(context.Background(), TokenRequest{User: "mona-cat_octo", Host: "tenant.ghe.com", TokenCommand: broker})
	if err != nil || token != "selected" {
		t.Fatalf("managed broker: %q, %v", token, err)
	}
	if err := VerifyTokenIdentity(context.Background(), "mona-cat_octo", "tenant.ghe.com", token); err != nil {
		t.Fatalf("managed identity: %v", err)
	}
}

func TestHostFallbackIgnoresWrongClass(t *testing.T) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
		t.Setenv(key, "")
	}
	t.Setenv("GH_TOKEN", "cloud")
	t.Setenv("GH_ENTERPRISE_TOKEN", "enterprise")
	for _, tc := range []struct{ host, token, source string }{{"", "cloud", "GH_TOKEN"}, {"a.ghe.com", "cloud", "GH_TOKEN"}, {"git.example.com", "enterprise", "GH_ENTERPRISE_TOKEN"}} {
		token, source, err := GetTokenWithFallbackForHost(context.Background(), TokenRequest{User: "alice", Host: tc.host})
		if err != nil || token != tc.token || source != tc.source {
			t.Fatalf("%q: %q %q %v", tc.host, token, source, err)
		}
	}
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "/nonexistent")
	if _, _, err := GetTokenWithFallbackForHost(context.Background(), TokenRequest{User: "alice", Host: "git.example.com"}); err == nil {
		t.Fatal("used cloud token for enterprise")
	}
	t.Setenv("GH_ENTERPRISE_TOKEN", "enterprise")
	t.Setenv("GH_TOKEN", "")
	if _, _, err := GetTokenWithFallbackForHost(context.Background(), TokenRequest{User: "alice"}); err == nil {
		t.Fatal("used enterprise token for github.com")
	}
}

func TestStreamingBrokerAndVerificationTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "command")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nwhile :; do printf '01234567890123456789012345678901'; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := ResolveToken(ctx, TokenRequest{User: "alice", TokenCommand: path}); err == nil {
		t.Fatal("accepted streaming output")
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatal("streaming output was not promptly stopped")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", path)
	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer verifyCancel()
	if err := VerifyTokenIdentity(verifyCtx, "alice", "github.com", "secret"); err == nil {
		t.Fatal("verification timeout accepted")
	}
}

func TestVerificationInvokesRealGHNotShim(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	shimDir := filepath.Join(home, ".local", "bin")
	realDir := t.TempDir()
	if err := os.MkdirAll(shimDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "shim-ran")
	t.Setenv("TEST_SHIM_MARKER", marker)
	if err := os.WriteFile(filepath.Join(shimDir, "gh"), []byte("#!/bin/sh\nprintf shim > \"$TEST_SHIM_MARKER\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(home, "real-ran")
	t.Setenv("TEST_REAL_LOG", log)
	real := "#!/bin/sh\nprintf 'called\\n' >> \"$TEST_REAL_LOG\"\ntest \"$*\" = 'api --hostname github.com user --jq .login' || exit 1\nprintf 'alice\\n'\n"
	if err := os.WriteFile(filepath.Join(realDir, "gh"), []byte(real), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)
	if err := VerifyTokenIdentity(context.Background(), "alice", "github.com", "selected"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shim launched: %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "called\n" {
		t.Fatalf("real invocation log %q: %v", data, err)
	}
}

func TestBrokerAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
test -z "${GH_ENTERPRISE_TOKEN+x}${GITHUB_ENTERPRISE_TOKEN+x}${GH_HOST+x}${GH_TOKEN+x}${GITHUB_TOKEN+x}" || exit 90
test "$GH_AUTOPROFILE_USER" = alice || exit 91
test "$GH_AUTOPROFILE_HOST" = git.example.com || exit 92
printf '%s' "$TEST_OUTPUT"
printf 'SECRET-STDERR' >&2
exit "$TEST_EXIT"
`
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "ambient")
	t.Setenv("GH_HOST", "other")
	t.Setenv("TEST_EXIT", "0")
	req := TokenRequest{User: "alice", Host: "GIT.EXAMPLE.COM", TokenCommand: path}
	for _, tc := range []struct {
		output string
		ok     bool
	}{{"broker-token\n", true}, {"x y", false}, {"x\ny", false}, {"x\t", false}, {strings.Repeat("x", maxCredentialOutput+1), false}} {
		t.Setenv("TEST_OUTPUT", tc.output)
		tok, err := ResolveToken(context.Background(), req)
		if (err == nil) != tc.ok || tc.ok && tok != "broker-token" {
			t.Fatalf("broker result: %q %v", tok, err)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Fatal("stderr leaked")
		}
	}
	t.Setenv("TEST_EXIT", "1")
	if _, _, err := GetTokenWithFallbackForHost(context.Background(), req); err == nil {
		t.Fatal("broker failure fell back")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := ResolveToken(ctx, req); err == nil {
		t.Fatal("timeout accepted")
	}
	verify := `#!/bin/sh
test "$*" = 'api --hostname git.example.com user --jq .login' || exit 91
test "$GH_ENTERPRISE_TOKEN" = selected && test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}${GITHUB_ENTERPRISE_TOKEN+x}${GH_DEBUG+x}" || exit 92
test "$GH_HOST" = git.example.com && test "$GH_PROMPT_DISABLED" = 1 || exit 93
printf '%s' "$TEST_OUTPUT"
exit "$TEST_EXIT"
`
	if err := os.WriteFile(path, []byte(verify), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", path)
	t.Setenv("GH_DEBUG", "api")
	t.Setenv("TEST_EXIT", "0")
	for _, tc := range []struct {
		output string
		ok     bool
	}{{"ALICE\n", true}, {"bob\n", false}, {"alice\nextra\n", false}} {
		t.Setenv("TEST_OUTPUT", tc.output)
		err := VerifyTokenIdentity(context.Background(), "alice", "git.example.com", "selected")
		if (err == nil) != tc.ok {
			t.Fatalf("verify %q: %v", tc.output, err)
		}
	}
	t.Setenv("TEST_EXIT", "1")
	if VerifyTokenIdentity(context.Background(), "alice", "git.example.com", "selected") == nil {
		t.Fatal("verify failure accepted")
	}
}

func TestResolveGHPath_SkipsAutoprofileShimDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")

	shimDir := filepath.Join(home, ".local", "bin")
	realDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(shimDir, 0755); err != nil {
		t.Fatalf("cannot create shim dir: %v", err)
	}
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatalf("cannot create real dir: %v", err)
	}

	shimPath := filepath.Join(shimDir, "gh")
	realPath := filepath.Join(realDir, "gh")
	if err := os.WriteFile(shimPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("cannot write shim gh: %v", err)
	}
	if err := os.WriteFile(realPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("cannot write real gh: %v", err)
	}

	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+realDir)

	path, err := ResolveGHPath()
	if err != nil {
		t.Fatalf("ResolveGHPath failed: %v", err)
	}
	if path != realPath {
		t.Fatalf("ResolveGHPath() = %q, want %q", path, realPath)
	}
}

func TestGetTokenIsolatedAndValidated(t *testing.T) {
	gh := filepath.Join(t.TempDir(), "gh")
	script := `#!/bin/sh
test -z "${GH_TOKEN+x}${GITHUB_TOKEN+x}" || exit 91
test "$*" = 'auth token --user alice --hostname github.com' || exit 92
printf '%s' "$TEST_TOKEN_OUTPUT"
exit "$TEST_TOKEN_EXIT"
`
	if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	t.Setenv("GH_TOKEN", "unrelated")
	t.Setenv("GITHUB_TOKEN", "also-unrelated")
	t.Setenv("GH_HOST", "enterprise.example.com")
	for _, tc := range []struct {
		name, output, exit, want string
	}{
		{"success", "stored-token\n", "0", "stored-token"},
		{"crlf", "stored-token\r\n", "0", ""},
		{"empty", "", "0", ""},
		{"whitespace", " \t\n", "0", ""},
		{"partial failure", "secret-partial\n", "1", ""},
		{"injection", "secret\npassword=injected\n", "0", ""},
		{"carriage return", "secret\rusername=injected\n", "0", ""},
		{"nul", "secret\x00bad\n", "0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Environment variables cannot contain NUL; test validation directly.
			if strings.ContainsRune(tc.output, 0) {
				if ValidateToken(tc.output) == nil {
					t.Fatal("accepted NUL")
				}
				return
			}
			t.Setenv("TEST_TOKEN_OUTPUT", tc.output)
			t.Setenv("TEST_TOKEN_EXIT", tc.exit)
			got, err := GetToken("alice")
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("GetToken = %q, %v; want %q", got, err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("error disclosed token output")
			}
		})
	}
}

func TestGetTokenWithFallbackRejectsProtocolInjection(t *testing.T) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("GH_TOKEN", "")
			t.Setenv("GITHUB_TOKEN", "")
			t.Setenv(key, "secret\npassword=injected")
			if _, _, err := GetTokenWithFallback("alice"); err == nil {
				t.Fatal("accepted injected credential field")
			}
		})
	}
}

func TestResolveGHRejectsSelfAliasesAndInvalidOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "gh")
	if err := os.Symlink(self, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", home)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	if _, err := ResolveGHPath(); err == nil {
		t.Fatal("self alias accepted on PATH")
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", alias)
	if _, err := ResolveGHPath(); err == nil {
		t.Fatal("self override accepted")
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "relative/gh")
	if _, err := ResolveGHPath(); err == nil {
		t.Fatal("relative override accepted")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(self, alias); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	if _, err := ResolveGHPath(); err == nil {
		t.Fatal("self hardlink accepted")
	}
}

func TestResolveGHAcceptsBinaryContainingMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path := filepath.Join(home, "gh")
	if err := os.WriteFile(path, []byte("\x7fELF gh-autoprofile-gh-shim __shim-gh"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", home)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	got, err := ResolveGHPath()
	if err != nil || got != path {
		t.Fatalf("got %q, %v", got, err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", path)
	if got, err := ResolveGHPath(); err != nil || got != path {
		t.Fatalf("override got %q, %v", got, err)
	}
}

func TestResolveGHRelativePATHSurvivesDirectoryChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(bin, "gh")
	if err := os.WriteFile(want, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(home); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	t.Setenv("PATH", "bin")
	got, err := ResolveGHPath()
	if err != nil || got != want {
		t.Fatalf("relative PATH resolved to %q, %v; want %q", got, err, want)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", got)
	if err := os.Chdir(bin); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveGHPath(); err != nil || got != want {
		t.Fatalf("exported override after directory change = %q, %v", got, err)
	}
}

func TestResolveGHOverrideUnmanagedDefaultShimPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	path := filepath.Join(home, ".local", "bin", "gh")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", path)
	if got, err := ResolveGHPath(); err != nil || got != path {
		t.Fatalf("unmanaged real gh override = %q, %v", got, err)
	}
}

// A gh-only shim whose target is not the running binary (for example after an
// upgrade) looks like an ordinary executable; resolution must still skip it, or
// the shim would exec itself.
func TestResolveGHPathSkipsGHOnlyShim(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native unsupported")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("GH_PATH", "")
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	ext := filepath.Join(root, "extension")
	real := filepath.Join(root, "real", "gh")
	if err := os.MkdirAll(filepath.Dir(real), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ext, real} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	shim, err := shims.InstallMiseGH(ext)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(shim)+string(os.PathListSeparator)+filepath.Dir(real))
	if got, err := ResolveGHPath(); err != nil || got != real {
		t.Fatalf("resolved %q: %v", got, err)
	}
}

func TestStaleGHOnlyShimIsNeverAcceptedAsRealGH(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native unsupported")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("GH_PATH", "")
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "")
	ext := filepath.Join(root, "old-extension")
	real := filepath.Join(root, "real", "gh")
	if err := os.MkdirAll(filepath.Dir(real), 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{ext, real} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	shim, err := shims.InstallMiseGH(ext)
	if err != nil {
		t.Fatal(err)
	}
	// Orphan the link, as after an upgrade that rewrote the ownership record.
	record := filepath.Join(root, "config", "gh-autoprofile", "native-gh-targets.json")
	if err := os.WriteFile(record, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	linkedDir := filepath.Join(root, "linked")
	if err := os.Symlink(filepath.Dir(shim), linkedDir); err != nil {
		t.Fatal(err)
	}
	viaLink := filepath.Join(linkedDir, "gh")
	elsewhere := filepath.Join(root, "alias-gh")
	if err := os.Symlink(shim, elsewhere); err != nil {
		t.Fatal(err)
	}

	for _, candidate := range []string{shim, viaLink, elsewhere} {
		for _, env := range []string{"GH_PATH", "GH_AUTOPROFILE_REAL_GH"} {
			t.Run(env+"/"+filepath.Base(candidate), func(t *testing.T) {
				t.Setenv(env, candidate)
				if got, err := ResolveGHPath(); err == nil || got != "" {
					t.Fatalf("stale shim accepted: %q", got)
				}
			})
		}
	}
	t.Run("symlinked PATH directory", func(t *testing.T) {
		t.Setenv("PATH", linkedDir+string(os.PathListSeparator)+filepath.Dir(real))
		if got, err := ResolveGHPath(); err != nil || got != real {
			t.Fatalf("resolved %q: %v", got, err)
		}
	})
}
