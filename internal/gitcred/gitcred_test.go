package gitcred

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestConfigureHostsAndRestore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	hosts, err := Hosts(&config.PinRegistry{Pins: []config.Pin{{Host: "GHE.EXAMPLE.COM"}, {User: "alice"}, {User: "alice", Host: "ghe.example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(hosts, ",") != "ghe.example.com,github.com" {
		t.Fatal(hosts)
	}
	if err := replaceHelpersHost("ghe.example.com", []string{"discarded", "", "first", "second"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := SnapshotHosts(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConfigureHosts(hosts); err != nil {
		t.Fatal(err)
	}
	if !CheckConfiguredHost("GHE.EXAMPLE.COM") || !CheckConfigured() {
		t.Fatal("host registration missing")
	}
	path, _ := HelperPath()
	got, err := HelperSummaryHost("ghe.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"", primaryHelper(path), "first", "second", helperFallback}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %q want %q", got, want)
	}
	if err := RestoreHosts(snapshot); err != nil {
		t.Fatal(err)
	}
	got, err = configuredHelpersHost("ghe.example.com")
	if err != nil || strings.Join(got, "\n") != "discarded\n\nfirst\nsecond" {
		t.Fatalf("restore %q: %v", got, err)
	}
}

func TestInstall(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	path, err := Install("/tmp/gh-autoprofile", "/tmp/gh")
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read helper: %v", err)
	}
	if string(data) == "" {
		t.Fatal("helper file is empty")
	}
	if got := filepath.Base(path); got != "git-credential-helper" {
		t.Fatalf("unexpected helper path %q", path)
	}
}

func TestInstallRefusesUnmanagedAndMigratesLegacy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path, err := HelperPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unmanaged\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("/tmp/extension", "/tmp/gh"); err == nil {
		t.Fatal("overwrote unmanaged helper")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "unmanaged\n" {
		t.Fatalf("helper changed: %q %v", got, err)
	}
	legacy := "#!/usr/bin/env bash\nset -euo pipefail\nexec '/old/extension' __git-credential \"$@\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("/tmp/extension's", "/tmp/gh's"); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("/tmp/extension's", "/tmp/gh's"); err != nil {
		t.Fatalf("cannot refresh managed helper: %v", err)
	}
	prior := "#!/bin/sh\n# gh-autoprofile-git-credential-helper\nGH_AUTOPROFILE_REAL_GH='/old/gh' exec '/old/extension' __git-credential \"$@\"\n"
	if err := os.WriteFile(path, []byte(prior), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("/tmp/extension's", "/tmp/gh's"); err != nil {
		t.Fatalf("cannot migrate previous helper: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Install("/tmp/extension", "/tmp/gh"); err == nil {
		t.Fatal("overwrote helper symlink")
	}
}

func TestRealGH(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := Install("/tmp/extension", "/opt/gh's/bin/gh"); err != nil {
		t.Fatal(err)
	}
	if got, err := RealGH(); err != nil || got != "/opt/gh's/bin/gh" {
		t.Fatalf("RealGH = %q, %v", got, err)
	}
	path, err := HelperPath()
	if err != nil {
		t.Fatal(err)
	}
	legacy := "#!/usr/bin/env bash\nset -euo pipefail\nexec '/old/extension' __git-credential \"$@\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := RealGH(); err != nil || got != "" {
		t.Fatalf("legacy helper RealGH = %q, %v; want none recorded", got, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec /somewhere/else\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := RealGH(); err == nil {
		t.Fatal("unmanaged helper accepted")
	}
}

func TestInstalledHelperOverridesBothGHPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("GH_PATH", "/attacker/gh")
	t.Setenv("GH_AUTOPROFILE_REAL_GH", "/attacker/gh")
	binary := filepath.Join(dir, "extension's")
	script := "#!/bin/sh\nprintf '%s\\n' \"$GH_PATH\" \"$GH_AUTOPROFILE_REAL_GH\" \"$@\"\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	realGH := filepath.Join(dir, "gh's trusted")
	helper, err := Install(binary, realGH)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"get", "store", "erase"} {
		out, err := exec.Command(helper, action).Output()
		want := realGH + "\n" + realGH + "\n__git-credential\n" + action + "\n"
		if err != nil || string(out) != want {
			t.Fatalf("%s: %q, %v; want %q", action, out, err, want)
		}
	}
}

func TestConfigureGitPreservesHelperActions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "space and quote's"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("TEST_ACTION_LOG", filepath.Join(dir, "actions"))
	binary := filepath.Join(dir, "fake-extension")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_ACTION_LOG\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(binary, "/tmp/gh"); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	if !CheckConfigured() {
		t.Fatal("quoted helper path not detected")
	}
	// Remove the real gh fallback so this test never accesses stored credentials.
	remove := exec.Command("git", "config", "--global", "--fixed-value", "--unset-all", helperSection+".helper", helperFallback)
	if out, err := remove.CombinedOutput(); err != nil {
		t.Fatalf("remove fallback: %v\n%s", err, out)
	}
	for _, action := range []string{"approve", "reject"} {
		cmd := exec.Command("git", "credential", action)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\nusername=test\npassword=test\n\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", action, err, out)
		}
	}
	out, err := os.ReadFile(filepath.Join(dir, "actions"))
	if err != nil || string(out) != "__git-credential store\n__git-credential erase\n" {
		t.Fatalf("helper actions: %q, %v", out, err)
	}
}

func TestConfigureGitPreservesExistingCustomHelpers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	if _, err := Install(filepath.Join(dir, "gh-autoprofile"), "/tmp/gh"); err != nil {
		t.Fatal(err)
	}
	key := helperSection + ".helper"
	for _, helper := range []string{"custom-one", "!custom-two get", helperFallback} {
		cmd := exec.Command("git", "config", "--global", "--add", key, helper)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("seed helper: %v\n%s", err, out)
		}
	}
	if err := ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	helpers, err := configuredHelpers()
	if err != nil {
		t.Fatal(err)
	}
	path, err := HelperPath()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"", primaryHelper(path), "custom-one", "!custom-two get", helperFallback}
	if strings.Join(helpers, "\n") != strings.Join(want, "\n") {
		t.Fatalf("helpers = %q, want %q", helpers, want)
	}
}

func TestInstallUsesLiteralExecutablePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	name := "extension ' $(touch dollar-ran) `touch backtick-ran` $HOME"
	binary := filepath.Join(dir, name)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	helper, err := Install(binary, "/tmp/gh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helper, "get", "literal arg")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "__git-credential\nget\nliteral arg\n" {
		t.Errorf("helper output %q, error %v", out, err)
	}
	for _, marker := range []string{"dollar-ran", "backtick-ran"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); !os.IsNotExist(err) {
			t.Errorf("path executed shell substitution %s: %v", marker, err)
		}
	}
}

func TestCheckConfiguredMatchesCurrentAndLegacyEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	path, err := HelperPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		entry string
		want  bool
	}{
		{primaryHelper(path), true},
		{"!" + path + " get", true},
		{primaryHelper(path + "-unrelated"), false},
		{"!printf " + shellQuote(path), false},
	} {
		cmd := exec.Command("git", "config", "--global", "--replace-all", helperSection+".helper", tc.entry)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("config: %v\n%s", err, out)
		}
		if got := CheckConfigured(); got != tc.want {
			t.Errorf("CheckConfigured(%q) = %v, want %v", tc.entry, got, tc.want)
		}
	}
}

func isolatedGlobalGit(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "gitconfig"))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
}

func TestConfigureHostsEnablesUseHTTPPathAndRestoresPriorValues(t *testing.T) {
	isolatedGlobalGit(t)
	hosts := []string{"ghe.example.com", "github.com", "other.example.com"}
	// Prior state: explicit false, absent, and an unrelated setting that must survive.
	if err := replaceGlobalValues(useHTTPPathKey("github.com"), []string{"false"}); err != nil {
		t.Fatal(err)
	}
	if err := replaceGlobalValues(useHTTPPathKey("other.example.com"), []string{"yes"}); err != nil {
		t.Fatal(err)
	}
	if err := replaceGlobalValues("user.name", []string{"Unrelated"}); err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts {
		if CheckUseHTTPPathHost(host) && host != "other.example.com" {
			t.Fatalf("%s unexpectedly enabled before configuration", host)
		}
	}
	snapshot, err := SnapshotHosts(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConfigureHosts(hosts, hosts...); err != nil {
		t.Fatal(err)
	}
	for _, host := range hosts {
		if !CheckUseHTTPPathHost(host) {
			t.Fatalf("useHttpPath missing for %s", host)
		}
		got, err := globalValues(useHTTPPathKey(host))
		if err != nil || len(got) != 1 || got[0] != "true" {
			t.Fatalf("%s useHttpPath = %q (%v), want exactly one true", host, got, err)
		}
	}
	// Reconfiguring is idempotent and does not duplicate entries.
	if err := ConfigureHosts(hosts, hosts...); err != nil {
		t.Fatal(err)
	}
	if got, _ := globalValues(useHTTPPathKey("github.com")); len(got) != 1 {
		t.Fatalf("duplicate useHttpPath entries: %q", got)
	}
	if err := RestoreHosts(snapshot); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string][]string{"github.com": {"false"}, "other.example.com": {"yes"}, "ghe.example.com": nil} {
		got, err := globalValues(useHTTPPathKey(host))
		if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s restored to %q (%v), want %q", host, got, err, want)
		}
	}
	if got, _ := globalValues("user.name"); len(got) != 1 || got[0] != "Unrelated" {
		t.Fatalf("unrelated config changed: %q", got)
	}
	if CheckUseHTTPPathHost("github.com") || CheckUseHTTPPathHost("not a host") {
		t.Fatal("check reports enabled after restore or for an invalid host")
	}
}

func TestConfigureHostsLeavesUseHTTPPathAloneWithoutOwnerPins(t *testing.T) {
	isolatedGlobalGit(t)
	if err := replaceGlobalValues(useHTTPPathKey("github.com"), []string{"false"}); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureHosts([]string{"github.com", "ghe.example.com"}); err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{"github.com": "false", "ghe.example.com": ""} {
		got, err := globalValues(useHTTPPathKey(host))
		if err != nil || strings.Join(got, ",") != want {
			t.Fatalf("%s useHttpPath = %q (%v), want %q", host, got, err, want)
		}
	}
	// Enabling is limited to the named hosts, and later calls never unset it.
	if err := ConfigureHosts([]string{"github.com", "ghe.example.com"}, "ghe.example.com"); err != nil {
		t.Fatal(err)
	}
	if CheckUseHTTPPathHost("github.com") || !CheckUseHTTPPathHost("ghe.example.com") {
		t.Fatal("useHttpPath enabled on the wrong hosts")
	}
	if err := ConfigureHosts([]string{"github.com", "ghe.example.com"}); err != nil {
		t.Fatal(err)
	}
	if !CheckUseHTTPPathHost("ghe.example.com") {
		t.Fatal("a later configure without owner pins unset useHttpPath")
	}
}

func TestOwnerHosts(t *testing.T) {
	hosts, err := OwnerHosts(&config.PinRegistry{Pins: []config.Pin{{Dir: "/a", User: "a"}, {Owner: "o", User: "b"}, {Owner: "p", User: "c", Host: "GHE.example.com"}}})
	if err != nil || strings.Join(hosts, ",") != "github.com,ghe.example.com" {
		t.Fatalf("%v %v", hosts, err)
	}
}

func TestOverwritingExplicitFalseIsAnnounced(t *testing.T) {
	isolatedGlobalGit(t)
	if err := replaceGlobalValues(useHTTPPathKey("github.com"), []string{"false"}); err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	err = ConfigureHosts([]string{"github.com", "ghe.example.com"}, "github.com", "ghe.example.com")
	os.Stderr = old
	w.Close()
	var buf [1024]byte
	n, _ := r.Read(buf[:])
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	out := string(buf[:n])
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "credential.https://github.com.useHttpPath=false") {
		t.Fatalf("want exactly one overwrite note for github.com, got %q", out)
	}
}

func TestTargetRoundTripsQuotedPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	want := "/opt/it's here/gh-autoaccount"
	if _, err := Install(want, "/usr/bin/gh"); err != nil {
		t.Fatal(err)
	}
	if got, err := Target(); err != nil || got != want {
		t.Fatalf("Target() = %q, %v; want %q", got, err, want)
	}
}
