package profileexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestHostCredentialEnvironments(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "old")
	t.Setenv("GITHUB_TOKEN", "old")
	t.Setenv("GH_ENTERPRISE_TOKEN", "old")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "old")
	t.Setenv("GH_HOST", "old.example")
	for _, tc := range []struct{ host, primary, fallback string }{
		{"github.com", "GH_TOKEN", "GITHUB_TOKEN"},
		{"tenant.ghe.com", "GH_TOKEN", "GITHUB_TOKEN"},
		{"enterprise.example", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			ctx := &Context{Pin: &config.Pin{User: "alice", Host: tc.host}, Token: "selected"}
			for _, execMode := range []bool{false, true} {
				var env []string
				var err error
				if execMode {
					env, err = BuildExecEnv(ctx)
				} else {
					env, err = BuildEnv(ctx, false)
				}
				if err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"} {
					want := ""
					if key == tc.primary || execMode && key == tc.fallback {
						want = "selected"
					}
					if got := envValue(env, key); got != want {
						t.Errorf("exec=%v %s=%q want %q", execMode, key, got, want)
					}
				}
				wantHost := tc.host
				if tc.host == "github.com" {
					wantHost = ""
				}
				if got := envValue(env, "GH_HOST"); got != wantHost {
					t.Errorf("GH_HOST=%q want %q", got, wantHost)
				}
			}
		})
	}
}

func TestSetEnvRemovesDuplicateMarkers(t *testing.T) {
	env := setEnv([]string{"GH_AUTOPROFILE_USER=old", "OTHER=keep", "GH_AUTOPROFILE_USER=older"}, "GH_AUTOPROFILE_USER", "alice")
	if strings.Join(env, ",") != "OTHER=keep,GH_AUTOPROFILE_USER=alice" {
		t.Fatalf("duplicate marker not removed: %v", env)
	}
}

func TestUnpinnedEnvironmentPassThroughAndStaleCleanup(t *testing.T) {
	t.Setenv("GH_TOKEN", "ambient")
	t.Setenv("GITHUB_TOKEN", "fallback")
	t.Setenv("GH_ENTERPRISE_TOKEN", "enterprise")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "enterprise-fallback")
	t.Setenv("GH_HOST", "enterprise.example")
	t.Setenv("GH_AUTOPROFILE_USER", "")
	t.Setenv("GH_AUTOPROFILE_HOST", "")
	t.Setenv("GH_AUTOPROFILE_MODE", "")
	env, err := BuildEnv(&Context{}, false)
	if err != nil || envValue(env, "GH_TOKEN") != "ambient" || envValue(env, "GH_HOST") != "enterprise.example" {
		t.Fatalf("pass through: %v", err)
	}
	t.Setenv("GH_AUTOPROFILE_HOST", "stale.example")
	env, err = BuildEnv(&Context{}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_AUTOPROFILE_USER", "GH_AUTOPROFILE_HOST", "GH_AUTOPROFILE_MODE"} {
		for _, entry := range env {
			if strings.HasPrefix(entry, key+"=") {
				t.Fatalf("stale %s retained", key)
			}
		}
	}
}

func TestNativeEnvRemovesInheritedManagedGitHelper(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	env, err := WithGitHubCredentials([]string{"GIT_CONFIG_COUNT=0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		t.Setenv(key, value)
	}
	ctx := &Context{Pin: &config.Pin{User: "alice", Host: "tenant.ghe.com"}, Token: "selected"}
	result, err := BuildEnv(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(result, gitHubConfigStart) != "" || envValue(result, "GIT_CONFIG_COUNT") != "0" {
		t.Fatal("inherited github.com credential helper retained")
	}
}

func TestBuildEnv_ManagedIdentityUsesNativeGit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_AUTHOR_NAME", "Explicit Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "explicit@example.test")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	if err := config.SaveSettings(config.Settings{GitIdentity: true}); err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Pin: &config.Pin{User: "alice", GitName: "Pin", GitEmail: "pin@example.test"}, Token: "valid-token"}
	for _, forGit := range []bool{false, true} {
		env, err := buildEnv(ctx, forGit)
		if err != nil {
			t.Fatal(err)
		}
		if envValue(env, "GIT_AUTHOR_NAME") != "Explicit Author" || envValue(env, "GIT_AUTHOR_EMAIL") != "explicit@example.test" {
			t.Fatal("explicit identity environment overridden")
		}
		for _, entry := range env {
			if strings.HasPrefix(entry, "GIT_CONFIG_KEY_") && (strings.HasSuffix(entry, "=user.email") || strings.HasSuffix(entry, "=user.name")) {
				t.Fatalf("injected identity config: %s", entry)
			}
		}
	}
}

func TestBuildEnv_InjectsTokenAndIdentity(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := &Context{
		Dir: "/tmp/project",
		Pin: &config.Pin{
			User:     "alice",
			GitEmail: "alice@example.com",
			GitName:  "Alice",
			SSHKey:   "/tmp/id_ed25519",
		},
		Token: "gho_testtoken",
	}

	env, err := buildEnv(ctx, false)
	if err != nil {
		t.Fatalf("buildEnv failed: %v", err)
	}

	want := map[string]string{
		"GH_AUTOPROFILE_USER": "alice",
		"GH_TOKEN":            "gho_testtoken",
		"GIT_AUTHOR_EMAIL":    "alice@example.com",
		"GIT_COMMITTER_EMAIL": "alice@example.com",
		"GIT_AUTHOR_NAME":     "Alice",
		"GIT_COMMITTER_NAME":  "Alice",
		"GIT_SSH_COMMAND":     "ssh -i '/tmp/id_ed25519' -o IdentitiesOnly=yes",
	}

	for key, expected := range want {
		if got := envValue(env, key); got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}
}

func TestInlineGitHelperDoesNotInterpretToken(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_ASKPASS", "/bin/false")
	token := `literal\npassword=injected*`
	ctx := &Context{Dir: t.TempDir(), Pin: &config.Pin{User: "alice"}, Token: token}
	env, err := buildEnv(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "credential", "fill")
	cmd.Dir = ctx.Dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "protocol=https\nhost=github.com\nusername=x-access-token\npassword=" + token + "\n"
	if string(out) != want {
		t.Fatalf("credential protocol changed token: %q, want %q", out, want)
	}
}

func TestGitHubHelperChecksRequestEvenWithoutURLScoping(t *testing.T) {
	env, err := WithGitHubCredentials([]string{"GH_TOKEN=selected-token", "GIT_CONFIG_COUNT=0"})
	if err != nil {
		t.Fatal(err)
	}
	helper := strings.TrimPrefix(envValue(env, "GIT_CONFIG_VALUE_1"), "!")
	for _, request := range []string{
		"protocol=http\nhost=github.com\n\n",
		"protocol=https\nhost=example.com\n\n",
		"protocol=https\nhost=github.com:8443\n\n",
		"protocol=ssh\nhost=github.com\n\n",
	} {
		cmd := exec.Command("sh", "-c", helper+" get")
		cmd.Env = env
		cmd.Stdin = strings.NewReader(request)
		if out, err := cmd.Output(); err != nil || len(out) != 0 {
			t.Fatalf("helper emitted credentials for %q: %q, %v", request, out, err)
		}
	}
}

func TestWithoutGitHubCredentialsPreservesCallerConfig(t *testing.T) {
	original := []string{
		"GH_TOKEN=token", "GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=caller-helper",
	}
	env, err := WithGitHubCredentials(append([]string{}, original...))
	if err != nil {
		t.Fatal(err)
	}
	env, err = appendGitConfigPairs(env, [][2]string{{"test.later", "keep-me"}})
	if err != nil {
		t.Fatal(err)
	}
	env, err = WithoutGitHubCredentials(env)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"GIT_CONFIG_COUNT": "2", "GIT_CONFIG_KEY_0": "credential.helper",
		"GIT_CONFIG_VALUE_0": "caller-helper", "GIT_CONFIG_KEY_1": "test.later",
		"GIT_CONFIG_VALUE_1": "keep-me", "GH_TOKEN": "token",
	} {
		if got := envValue(env, key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if got := envValue(env, gitHubConfigStart); got != "" {
		t.Fatalf("ownership marker retained: %q", got)
	}
	unmanaged, err := WithoutGitHubCredentials(original)
	if err != nil || strings.Join(unmanaged, "\n") != strings.Join(original, "\n") {
		t.Fatalf("unmanaged caller config changed: %v, %v", unmanaged, err)
	}
}

func TestBuildEnv_ForGitAppendsCredentialConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := &Context{
		Dir: "/tmp/project",
		Pin: &config.Pin{
			User:     "alice",
			GitEmail: "alice@example.com",
			GitName:  "Alice",
		},
		Token: "gho_testtoken",
	}

	env, err := buildEnv(ctx, true)
	if err != nil {
		t.Fatalf("buildEnv failed: %v", err)
	}

	if got := envValue(env, "GIT_CONFIG_COUNT"); got != "5" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want 5", got)
	}

	want := map[string]string{
		"GIT_CONFIG_KEY_0":   "credential.https://github.com.helper",
		"GIT_CONFIG_VALUE_0": "",
		"GIT_CONFIG_KEY_1":   "credential.https://github.com.helper",
		"GIT_CONFIG_KEY_2":   "credential.https://github.com.username",
		"GIT_CONFIG_VALUE_2": "x-access-token",
		"GIT_CONFIG_KEY_3":   "user.email",
		"GIT_CONFIG_VALUE_3": "alice@example.com",
		"GIT_CONFIG_KEY_4":   "user.name",
		"GIT_CONFIG_VALUE_4": "Alice",
	}

	for key, expected := range want {
		if got := envValue(env, key); got != expected {
			t.Errorf("%s = %q, want %q", key, got, expected)
		}
	}

	helper := envValue(env, "GIT_CONFIG_VALUE_1")
	if helper == "" || helper[:2] != "!f" {
		t.Fatalf("credential helper snippet not installed: %q", helper)
	}
}

func TestAppendGitConfigPairs_PreservesExistingEntries(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.sslverify")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")

	env, err := appendGitConfigPairs([]string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.sslverify",
		"GIT_CONFIG_VALUE_0=false",
	}, [][2]string{{"credential.helper", ""}})
	if err != nil {
		t.Fatalf("appendGitConfigPairs failed: %v", err)
	}

	if got := envValue(env, "GIT_CONFIG_COUNT"); got != "2" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want 2", got)
	}
	if got := envValue(env, "GIT_CONFIG_KEY_0"); got != "http.sslverify" {
		t.Fatalf("existing config entry lost: %q", got)
	}
	if got := envValue(env, "GIT_CONFIG_KEY_1"); got != "credential.helper" {
		t.Fatalf("new config key = %q, want credential.helper", got)
	}
}

func TestBuildSSHCommand_EscapesSingleQuotes(t *testing.T) {
	keyPath := filepath.Join("/tmp", "team's key")
	got := buildSSHCommand(keyPath)
	want := "ssh -i '/tmp/team'\\''s key' -o IdentitiesOnly=yes"
	if got != want {
		t.Fatalf("buildSSHCommand() = %q, want %q", got, want)
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if len(entry) > len(prefix) && entry[:len(prefix)] == prefix {
			return entry[len(prefix):]
		}
	}
	return ""
}

func TestBuildExecEnvKeepsGitConfigDistinctFromShim(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GH_AUTOPROFILE_GIT_CONFIG_START", "")
	if err := os.Unsetenv("GH_AUTOPROFILE_GIT_CONFIG_START"); err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Pin: &config.Pin{User: "alice", GitEmail: "alice@example.test", GitName: "Alice", SSHKey: "/tmp/key"}, Token: "synthetic"}
	explicit, err := BuildExecEnv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shim, err := BuildEnv(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if envValue(explicit, "GIT_CONFIG_COUNT") != "3" || envValue(shim, "GIT_CONFIG_COUNT") != "6" {
		t.Fatalf("explicit count %q, shim count %q", envValue(explicit, "GIT_CONFIG_COUNT"), envValue(shim, "GIT_CONFIG_COUNT"))
	}
	if envValue(explicit, "GIT_AUTHOR_EMAIL") != "alice@example.test" || envValue(explicit, "GH_TOKEN") != "synthetic" {
		t.Fatal("explicit execution lost identity or token")
	}
}
