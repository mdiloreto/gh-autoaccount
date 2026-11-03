package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestShouldBypassAuthInjection(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"auth", "switch", "--user", "alice"}, true},
		{[]string{"auth", "login"}, true},
		{[]string{"auth", "logout"}, true},
		{[]string{"auth", "refresh"}, true},
		{[]string{"auth", "setup-git"}, true},
		{[]string{"auth", "status"}, false},
		{[]string{"repo", "view"}, false},
		{[]string{"autoaccount", "default", "--unset"}, false},
	}
	for _, tt := range tests {
		if got := shouldBypassAuthInjection(tt.args); got != tt.want {
			t.Fatalf("shouldBypassAuthInjection(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestRealGHManagementAliasCannotBypassMissingCredential(t *testing.T) {
	const realGH = "/usr/bin/gh"
	if runtime.GOOS != "linux" {
		if os.Getenv("GH_AUTOPROFILE_REQUIRE_REAL_GH") == "1" {
			t.Fatal("real-gh acceptance requires Linux")
		}
		t.Skip("real-gh acceptance requires Linux")
	}
	if _, err := os.Stat(realGH); err != nil {
		if os.Getenv("GH_AUTOPROFILE_REQUIRE_REAL_GH") == "1" {
			t.Fatalf("real gh required: %v", err)
		}
		t.Skipf("real gh unavailable: %v", err)
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	bin := filepath.Join(root, "bin")
	ghConfig := filepath.Join(root, "gh-config")
	configDir := filepath.Join(root, "config", "gh-autoprofile")
	for _, dir := range []string{home, bin, ghConfig, configDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tool unavailable: %v", err)
	}
	originalHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("cannot determine original home: %v", err)
	}
	buildEnv := isolatedGoTestEnv(root, originalHome, os.Getenv("GOPATH"), os.Getenv("GOMODCACHE"))
	goEnv := exec.Command(goPath, "env", "GOMODCACHE")
	goEnv.Env = buildEnv
	goEnv.Dir = "../.."
	cacheOut, err := goEnv.Output()
	if err != nil {
		t.Fatalf("cannot determine module cache: %v", err)
	}
	moduleCache := strings.TrimSpace(string(cacheOut))
	if !filepath.IsAbs(moduleCache) {
		t.Fatal("module cache is not absolute")
	}
	candidate := filepath.Join(root, "candidate")
	build := exec.Command(goPath, "build", "-o", candidate, "./cmd/gh-autoaccount")
	build.Dir = "../.."
	build.Env = setEnv(buildEnv, "GOMODCACHE", moduleCache)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %s %v", out, err)
	}
	shim := filepath.Join(bin, "gh")
	if err := os.Symlink(candidate, shim); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(configDir, "settings.yml")
	if err := os.WriteFile(settings, []byte("native: true\ndefault_user: nonexistent-fixture-user\n"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "alias-ran")
	env := []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"GH_CONFIG_DIR=" + ghConfig, "GH_AUTOPROFILE_REAL_GH=" + realGH, "GH_PATH=" + realGH, "GH_EXTENSION=stale",
		"PATH=" + bin + ":/usr/bin:/bin", "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "GH_TELEMETRY=0",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(root, "no-bus"),
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1", "ALL_PROXY=http://127.0.0.1:1", "NO_PROXY=",
	}
	run := func(name string, args ...string) (string, string, int) {
		command := exec.Command(name, args...)
		command.Env, command.Dir = env, home
		command.Stdin = strings.NewReader("fixture input\n")
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if err == nil {
			return stdout.String(), stderr.String(), 0
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return stdout.String(), stderr.String(), exit.ExitCode()
		}
		t.Fatalf("%s: %v", name, err)
		return "", "", -1
	}
	alias := fmt.Sprintf("printf synthetic > '%s'", marker)
	if out, errout, code := run(realGH, "alias", "set", "--shell", "autoaccount", alias); code != 0 {
		t.Fatalf("alias setup: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
	stored, err := os.ReadFile(filepath.Join(ghConfig, "config.yml"))
	if err != nil || !strings.Contains(string(stored), "autoaccount:") || !strings.Contains(string(stored), "printf synthetic") {
		t.Fatalf("alias not stored: %q %v", stored, err)
	}
	selected, err := os.ReadFile(settings)
	if err != nil || !strings.Contains(string(selected), "default_user: nonexistent-fixture-user") {
		t.Fatalf("selected default missing: %q %v", selected, err)
	}
	controlOut, controlErr, controlCode := run(shim, "version")
	t.Logf("missing-credential control: stdout=%q stderr=%q exit=%d", controlOut, controlErr, controlCode)
	if controlCode == 0 {
		t.Fatal("control unexpectedly resolved missing credential")
	}
	relativeMarker := filepath.Join(root, "relative-extension-ran")
	relativeScript := "#!/bin/sh\nprintf bad > \"$FIXTURE_RELATIVE_MARKER\"\n"
	for _, target := range []string{
		filepath.Join(home, "gh", "extensions", "gh-autoaccount", "gh-autoaccount"),
		filepath.Join(home, ".local", "share", "gh", "extensions", "gh-autoaccount", "gh-autoaccount"),
	} {
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(relativeScript), 0700); err != nil {
			t.Fatal(err)
		}
	}
	originalEnv := append([]string(nil), env...)
	for _, tc := range []struct{ name, xdg, home string }{
		{"relative-xdg", ".", home}, {"relative-home", "", "."},
	} {
		env = setEnv(append([]string(nil), originalEnv...), "XDG_DATA_HOME", tc.xdg)
		env = setEnv(env, "HOME", tc.home)
		env = append(env, "FIXTURE_RELATIVE_MARKER="+relativeMarker)
		_, _, relativeCode := run(shim, "autoaccount", "default", "--unset")
		_, markerErr := os.Stat(relativeMarker)
		t.Logf("%s: exit=%d marker_exists=%t", tc.name, relativeCode, markerErr == nil)
		if relativeCode == 0 || !os.IsNotExist(markerErr) {
			t.Errorf("%s accepted an untrusted relative extension path", tc.name)
		}
		if markerErr == nil {
			if err := os.Remove(relativeMarker); err != nil {
				t.Fatal(err)
			}
		}
	}
	env = originalEnv
	out, errout, code := run(shim, "autoaccount", "default", "--unset")
	_, markerErr := os.Stat(marker)
	markerExists := markerErr == nil
	if markerErr != nil && !os.IsNotExist(markerErr) {
		t.Fatal(markerErr)
	}
	t.Logf("command: %s autoaccount default --unset", shim)
	t.Logf("stdout=%q stderr=%q exit=%d marker_exists=%t", out, errout, code, markerExists)
	t.Logf("stored cross-check: default_user=nonexistent-fixture-user; autoaccount shell alias present")
	if markerExists || code == 0 {
		t.Error("missing extension must fail without executing the management alias")
	}
	env = append(env, "GH_AUTOPROFILE_DISABLE_SHIM=1")
	_, _, disabledCode := run(shim, "autoaccount", "default", "--unset")
	env = env[:len(env)-1]
	_, disabledMarkerErr := os.Stat(marker)
	t.Logf("explicit disable: exit=%d marker_exists=%t", disabledCode, disabledMarkerErr == nil)
	if disabledCode != 0 || disabledMarkerErr != nil {
		t.Errorf("explicitly disabled shim did not dispatch the real gh alias: exit=%d marker_error=%v", disabledCode, disabledMarkerErr)
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	extDir := filepath.Join(root, "data", "gh", "extensions", "gh-autoaccount")
	if err := os.MkdirAll(extDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(candidate, filepath.Join(extDir, "gh-autoaccount")); err != nil {
		t.Fatal(err)
	}
	if out, errout, code := run(realGH, "alias", "set", "aa", "autoaccount"); code != 0 {
		t.Fatalf("short alias setup: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
	if err := os.Remove(filepath.Join(extDir, "gh-autoaccount")); err != nil {
		t.Fatal(err)
	}
	if _, _, code := run(shim, "aa", "default", "--unset"); code == 0 {
		t.Fatal("short alias dispatched without installed extension")
	}
	if err := os.Symlink(candidate, filepath.Join(extDir, "gh-autoaccount")); err != nil {
		t.Fatal(err)
	}
	shortOut, shortErr, shortCode := run(shim, "aa", "default", "--unset")
	if shortCode != 0 {
		t.Fatalf("short alias recovery: stdout=%q stderr=%q exit=%d", shortOut, shortErr, shortCode)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("competing literal alias ran: %v", err)
	}
	if err := os.WriteFile(settings, []byte("native: true\ndefault_user: nonexistent-fixture-user\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recoveryOut, recoveryErr, recoveryCode := run(shim, "autoaccount", "default", "--unset")
	t.Logf("genuine extension recovery: stdout=%q stderr=%q exit=%d", recoveryOut, recoveryErr, recoveryCode)
	if recoveryCode != 0 {
		t.Error("genuine extension recovery failed")
	}
	recovered, err := os.ReadFile(settings)
	if err != nil || strings.Contains(string(recovered), "default_user:") {
		t.Errorf("recovery did not unset selected default: %q %v", recovered, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("alias ran instead of extension: %v", err)
	}
	extTarget := filepath.Join(extDir, "gh-autoaccount")
	if err := os.Remove(extTarget); err != nil {
		t.Fatal(err)
	}
	newTarget := filepath.Join(root, "new-extension")
	script := "#!/bin/sh\nprintf '%s\\n' \"$GH_PATH\" \"$GH_EXTENSION\" \"${GH_TOKEN-unset}\" \"${GITHUB_TOKEN-unset}\" \"$PWD\"\nprintf '<%s>\\n' \"$@\"\nIFS= read -r input\nprintf 'stdin:%s\\n' \"$input\"\nprintf 'from-extension\\n' >&2\nexit 37\n"
	if err := os.WriteFile(newTarget, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newTarget, extTarget); err != nil {
		t.Fatal(err)
	}
	swappedOut, swappedErr, swappedCode := run(shim, "autoaccount", "space arg", "", "'literal$*'")
	want := realGH + "\n1\nunset\nunset\n" + home + "\n<space arg>\n<>\n<'literal$*'>\nstdin:fixture input\n"
	if swappedCode != 37 || swappedOut != want || swappedErr != "from-extension\n" {
		t.Errorf("replacement extension: stdout=%q stderr=%q exit=%d", swappedOut, swappedErr, swappedCode)
	}
	shortOut, shortErr, shortCode = run(shim, "aa", "space arg", "", "'literal$*'")
	if shortCode != 37 || shortOut != want || shortErr != "from-extension\n" {
		t.Errorf("short alias extension: stdout=%q stderr=%q exit=%d", shortOut, shortErr, shortCode)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("alias ran after replacement: %v", err)
	}
	if err := os.WriteFile(newTarget, []byte("#!/bin/sh\nkill -TERM $$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	signaled := exec.Command(shim, "autoaccount", "signal")
	signaled.Env, signaled.Dir = env, home
	if err := signaled.Run(); err == nil || err.Error() != "signal: terminated" {
		t.Errorf("extension signal was not propagated: %v", err)
	}
	signaled = exec.Command(shim, "aa", "signal")
	signaled.Env, signaled.Dir = env, home
	if err := signaled.Run(); err == nil || err.Error() != "signal: terminated" {
		t.Errorf("short alias signal was not propagated: %v", err)
	}
}

func TestShortAliasMalformedOrChangedCannotBypassCredential(t *testing.T) {
	for _, tc := range []struct{ name, aliases string }{
		{"shell", "aa: '!touch marker'\n"},
		{"extra args", "aa: autoaccount status\n"},
		{"whitespace", "aa: ' autoaccount'\n"},
		{"multiline", "other: |\n  aa: autoaccount\naa: bad\n"},
		{"malformed", "aa: [\n"},
		{"duplicate", "aa: autoaccount\naa: bad\n"},
		{"nonstring", "aa: 12\n"},
		{"list failure", "FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := onboardingHome(t)
			aliasFile := filepath.Join(home, "aliases")
			marker := filepath.Join(home, "ran")
			if err := os.WriteFile(aliasFile, []byte(tc.aliases), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_ALIAS_FILE", aliasFile)
			t.Setenv("TEST_MARKER", marker)
			gh := filepath.Join(home, "bin", "gh")
			script := "#!/bin/sh\nif [ \"$1 $2\" = 'alias list' ]; then [ \"$(cat \"$TEST_ALIAS_FILE\")\" != FAIL ] || exit 1; cat \"$TEST_ALIAS_FILE\"; exit 0; fi\nif [ \"$1\" = aa ]; then touch \"$TEST_MARKER\"; fi\nexit 1\n"
			if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
			if err := config.SaveSettings(config.Settings{DefaultUser: "nonexistent-fixture-user"}); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(home, "data", "gh", "extensions", "gh-autoaccount", "gh-autoaccount")
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte("#!/bin/sh\ntouch \"$TEST_MARKER\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			command := NewShimGHCMD()
			command.SetArgs([]string{"aa", "status"})
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			if err := command.Execute(); err == nil {
				t.Fatal("missing credential allowed dispatch")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("payload or extension ran: %v", err)
			}
		})
	}
}

func isolatedGoTestEnv(root, originalHome, goPathEnv, moduleCache string) []string {
	if goPathEnv == "" {
		goPathEnv = filepath.Join(originalHome, "go")
	}
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "GH_CONFIG_DIR=" + filepath.Join(root, "gh-config"),
		"GOCACHE=" + filepath.Join(root, "go-cache"), "GOPATH=" + goPathEnv,
		"GOENV=off", "GOWORK=off", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local",
	}
	if moduleCache != "" {
		env = append(env, "GOMODCACHE="+moduleCache)
	}
	return env
}

func TestIsolatedGoTestEnvModuleCache(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("real-gh fixture requires Linux")
	}
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	root, originalHome := t.TempDir(), t.TempDir()
	t.Setenv("GH_TOKEN", "should-not-be-inherited")
	t.Setenv("GOENV", filepath.Join(root, "untrusted-goenv"))
	for _, tc := range []struct{ name, gopath, moduleCache, want string }{
		{"fallback", "", "", filepath.Join(originalHome, "go", "pkg", "mod")},
		{"gopath", filepath.Join(root, "custom-gopath"), "", filepath.Join(root, "custom-gopath", "pkg", "mod")},
		{"explicit", filepath.Join(root, "custom-gopath"), filepath.Join(root, "modules"), filepath.Join(root, "modules")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := isolatedGoTestEnv(root, originalHome, tc.gopath, tc.moduleCache)
			for _, item := range env {
				if strings.HasPrefix(item, "GH_TOKEN=") || item == "GOENV="+filepath.Join(root, "untrusted-goenv") {
					t.Fatal("ambient configuration leaked")
				}
			}
			command := exec.Command(goPath, "env", "GOMODCACHE")
			command.Env = env
			out, err := command.Output()
			if err != nil || strings.TrimSpace(string(out)) != tc.want {
				t.Fatalf("module cache mismatch: %q %v", out, err)
			}
		})
	}
}

func TestManagementExtensionPathUsesGHDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("management dispatch requires Unix")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_CONFIG_DIR", filepath.Join(home, "unrelated-config"))
	t.Setenv("XDG_DATA_HOME", "")
	localDir := filepath.Join(home, "local-extension")
	if err := os.MkdirAll(localDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "gh-autoaccount"), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".local", "share", "gh", "extensions", "gh-autoaccount")
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(localDir, link); err != nil {
		t.Fatal(err)
	}
	got, err := managementExtensionPath()
	if err != nil || got != filepath.Join(link, "gh-autoaccount") {
		t.Fatalf("local symlink: %q %v", got, err)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg-data"))
	if _, err := managementExtensionPath(); err == nil {
		t.Fatal("missing XDG extension unexpectedly used fallback")
	}
	target := filepath.Join(home, "xdg-data", "gh", "extensions", "gh-autoaccount", "gh-autoaccount")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := managementExtensionPath(); err == nil {
		t.Fatal("non-executable extension accepted")
	}
	if err := os.Chmod(target, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := managementExtensionPath(); err != nil || got != target {
		t.Fatalf("XDG extension: %q %v", got, err)
	}
}

func TestShimAllowsDefaultRecoveryWhenCredentialUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("management dispatch requires Unix")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("GH_AUTOPROFILE_DISABLE_SHIM", "")
	real := filepath.Join(home, "gh")
	aliasLog := filepath.Join(home, "alias-list-called")
	t.Setenv("TEST_ALIAS_LOG", aliasLog)
	if err := os.WriteFile(real, []byte("#!/bin/sh\nif [ \"$1\" = alias ]; then touch \"$TEST_ALIAS_LOG\"; exit 1; fi\nif [ \"$1\" = auth ]; then exit 1; fi\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", real)
	t.Setenv("GH_PATH", real)
	target := filepath.Join(home, "data", "gh", "extensions", "gh-autoaccount", "gh-autoaccount")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\nprintf '%s\\n' \"autoaccount\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	cmd := NewShimGHCMD()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"autoaccount", "default", "--unset"})
	if err := cmd.Execute(); err != nil || out.String() != "autoaccount\ndefault\n--unset\n" {
		t.Fatalf("recovery bypass: %q %v", out.String(), err)
	}
	if _, err := os.Stat(aliasLog); !os.IsNotExist(err) {
		t.Fatalf("literal management queried aliases: %v", err)
	}
}

func TestShimBypassPreservesArgumentsAndEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GH_AUTOPROFILE_DISABLE_SHIM", "yes")
	t.Setenv("GH_TOKEN", "fake-test-token")
	t.Setenv("GH_AUTOPROFILE_USER", "fixture-user")
	t.Setenv("GH_EXTENSION", "caller-value")
	real := filepath.Join(home, "real gh")
	if err := os.WriteFile(real, []byte("#!/bin/sh\nprintf '%s\\n' \"$GH_TOKEN\" \"$GH_AUTOPROFILE_USER\" \"$GH_EXTENSION\" \"$@\"\n[ \"$1\" != autoaccount ] || exit 37\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", real)
	t.Setenv("GH_PATH", real)
	command := NewShimGHCMD()
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"--flag", "space argument"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "fake-test-token\nfixture-user\ncaller-value\n--flag\nspace argument\n" {
		t.Fatalf("output = %q", got)
	}
	command = NewShimGHCMD()
	command.SilenceUsage = true
	command.SilenceErrors = true
	out.Reset()
	command.SetOut(&out)
	command.SetArgs([]string{"autoaccount", "space argument", "", "'$*'"})
	err := command.Execute()
	exit, ok := err.(interface{ ExitCode() int })
	if !ok || exit.ExitCode() != 37 {
		t.Fatalf("disabled management exit = %v", err)
	}
	if got := out.String(); got != "fake-test-token\nfixture-user\ncaller-value\nautoaccount\nspace argument\n\n'$*'\n" {
		t.Fatalf("disabled management output = %q", got)
	}
	command = NewShimGHCMD()
	out.Reset()
	command.SetOut(&out)
	command.SetArgs([]string{"aa", "status"})
	if err := command.Execute(); err != nil {
		t.Fatalf("disabled short alias dispatch: %v", err)
	}
	if got := out.String(); got != "fake-test-token\nfixture-user\ncaller-value\naa\nstatus\n" {
		t.Fatalf("disabled short alias output = %q", got)
	}
}
