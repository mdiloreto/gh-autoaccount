package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAliasMapping(t *testing.T) {
	tests := []struct {
		name, input  string
		valid, exact bool
	}{
		{"empty", "{}\n", true, false},
		{"empty output", "", true, false},
		{"exact", "aa: autoaccount\n", true, true},
		{"quoted", "'aa': \"autoaccount\"\n", true, true},
		{"embedded", "other: |\n  something\n  aa: autoaccount\n", true, false},
		{"duplicate", "aa: autoaccount\naa: other\n", false, false},
		{"boolean key", "true: value\n", false, false},
		{"integer value", "aa: 3\n", false, false},
		{"boolean value", "aa: true\n", false, false},
		{"map value", "aa: {x: y}\n", false, false},
		{"malformed", "aa: [\n", false, false},
		{"documents", "aa: autoaccount\n---\nx: y\n", false, false},
		{"yaml alias", "x: &x autoaccount\naa: *x\n", false, false},
		{"whitespace", "aa: ' autoaccount'\n", true, false},
		{"args", "aa: autoaccount list\n", true, false},
		{"shell", "aa: '!autoaccount'\n", true, false},
		{"chain", "aa: other\nother: autoaccount\n", true, false},
		{"placeholder", "aa: 'autoaccount $1'\n", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := parseAliasMapping([]byte(tt.input))
			if (err == nil) != tt.valid || (err == nil && (m["aa"] == "autoaccount") != tt.exact) {
				t.Fatalf("mapping=%v error=%v", m, err)
			}
		})
	}
}

func fakeAliasGH(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestShortAliasFailures(t *testing.T) {
	for name, script := range map[string]string{
		"failure":   "exit 1",
		"oversized": "head -c 70000 /dev/zero",
		"hang":      "exec sleep 10",
	} {
		t.Run(name, func(t *testing.T) {
			gh := fakeAliasGH(t, script)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if hasShortAlias(ctx, gh) {
				t.Fatal("unexpected alias")
			}
			if _, err := configureShortAlias(ctx, gh); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if hasShortAlias(context.Background(), "relative-gh") {
		t.Fatal("relative path accepted")
	}
}

func TestShortAliasFake(t *testing.T) {
	for _, tc := range []struct {
		name, listing string
		wantErr       bool
	}{
		{"identical", "aa: autoaccount", false},
		{"collision", "aa: something-else", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gh := fakeAliasGH(t, "if [ \"$2\" = list ]; then printf '%s\\n' '"+tc.listing+"'; else exit 42; fi")
			created, err := configureShortAlias(context.Background(), gh)
			if created || (err != nil) != tc.wantErr {
				t.Fatalf("created=%v err=%v", created, err)
			}
		})
	}
	gh := fakeAliasGH(t, "if [ \"$2\" = list ]; then echo '{}'; else exit 1; fi")
	if _, err := configureShortAlias(context.Background(), gh); err == nil {
		t.Fatal("set failure accepted")
	}
	state := filepath.Join(t.TempDir(), "state")
	gh = fakeAliasGH(t, "if [ \"$2\" = set ]; then touch '"+state+"'; elif [ -e '"+state+"' ]; then echo 'aa: other'; else echo '{}'; fi")
	if created, err := configureShortAlias(context.Background(), gh); created || err == nil {
		t.Fatalf("unverified creation: created=%v err=%v", created, err)
	}
}

func TestShortAliasRealGH(t *testing.T) {
	gh := "/usr/bin/gh"
	if runtime.GOOS != "linux" {
		t.Skip("Linux integration test")
	}
	if _, err := os.Stat(gh); err != nil {
		if os.Getenv("GH_AUTOPROFILE_REQUIRE_REAL_GH") == "1" {
			t.Fatal(err)
		}
		t.Skip("real gh unavailable")
	}
	if os.Getenv("ALIAS_TEST_ISOLATED") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestShortAliasRealGH$")
		cmd.Env = []string{"ALIAS_TEST_ISOLATED=1", "PATH=/usr/bin:/bin", "LANG=C", "GH_PROMPT_DISABLED=1"}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated real gh test: %v\n%s", err, output)
		}
		return
	}
	aliasTimeout = time.Minute
	dir := t.TempDir()
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("GH_CONFIG_DIR", filepath.Join(dir, "ghconfig"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	ext := filepath.Join(dir, "data", "gh", "extensions", "gh-autoaccount", "gh-autoaccount")
	if err := os.MkdirAll(filepath.Dir(ext), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ext, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created, err := configureShortAlias(ctx, gh)
	if err != nil || !created || !hasShortAlias(ctx, gh) {
		t.Fatalf("created=%v err=%v", created, err)
	}
	created, err = configureShortAlias(ctx, gh)
	if err != nil || created {
		t.Fatalf("repeat created=%v err=%v", created, err)
	}
	cmd := exec.Command(gh, "alias", "set", "aa", "version")
	if err := cmd.Run(); err == nil {
		t.Fatal("real gh clobbered alias")
	}
	if !hasShortAlias(ctx, gh) {
		t.Fatal("alias changed")
	}
	cmd = exec.Command(gh, "alias", "delete", "aa")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("delete: %v %s", err, output)
	}
	marker := filepath.Join(dir, "executed")
	cmd = exec.Command(gh, "alias", "set", "aa", "!touch "+marker)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell alias: %v %s", err, output)
	}
	if hasShortAlias(ctx, gh) {
		t.Fatal("shell alias accepted")
	}
	if _, err := configureShortAlias(ctx, gh); err == nil {
		t.Fatal("collision accepted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell alias executed: %v", err)
	}
	data, err := runAliasGH(ctx, gh, "alias", "list")
	if err != nil || !strings.Contains(string(data), "!touch ") {
		t.Fatalf("collision changed: %v", err)
	}
	aliases, err := parseAliasMapping(data)
	if err != nil {
		t.Fatal(err)
	}
	for name := range aliases {
		cmd = exec.Command(gh, "alias", "delete", name)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("delete alias: %v %s", err, output)
		}
	}
	data, err = runAliasGH(ctx, gh, "alias", "list")
	if err != nil || strings.TrimSpace(string(data)) != "" {
		t.Fatalf("expected empty list: %q err=%v", data, err)
	}
	created, err = configureShortAlias(ctx, gh)
	after, listErr := runAliasGH(ctx, gh, "alias", "list")
	t.Logf("empty list=%q created=%v err=%v after=%q listErr=%v", data, created, err, after, listErr)
	if err != nil || !created || listErr != nil || !hasShortAlias(ctx, gh) {
		t.Fatal("failed to configure alias after empty list")
	}
}
