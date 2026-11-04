package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

func TestDoctorNativeRequiredIntegrations(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	if err := config.SaveSettings(config.Settings{Native: true, ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	for _, want := range []string{"routing mode: native", "direnv is optional", "WARN required native gh proxy", "WARN required git credential helper missing", "WARN required git credential helper not configured", "external PATH integration not verified", "runtime real gh:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shims.InstallNativeGH(exe); err != nil {
		t.Fatal(err)
	}
	helper, err := gitcred.Install(exe, filepath.Join(home, "bin", "gh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := gitcred.ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(helper, 0600); err != nil {
		t.Fatal(err)
	}
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "OK   native gh proxy installed") || !strings.Contains(out, "WARN required git credential helper missing or not executable") || !strings.Contains(out, "OK   git credential helper configured") {
		t.Fatal(out)
	}
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "OK   git credential helper installed") || !strings.Contains(out, "OK   git credential helper target: "+exe) {
		t.Fatal(out)
	}
	path := filepath.Join(home, ".local", "bin", "gh")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "missing"), path); err != nil {
		t.Fatal(err)
	}
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "WARN required native gh proxy missing, broken, or unmanaged") {
		t.Fatal(out)
	}
}

func TestDoctorEnterpriseHostHelperCoverage(t *testing.T) {
	home := onboardingHome(t)
	repo := filepath.Join(home, "enterprise")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	broker := filepath.Join(home, "private-broker-name")
	if err := os.WriteFile(broker, []byte("#!/bin/sh\nprintf sensitive-token\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{User: "alice", Dir: repo, Host: "ghe.example.com", TokenCommand: broker}}}); err != nil {
		t.Fatal(err)
	}
	if err := gitcred.ConfigureGit(); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "WARN git credential helper not configured for ghe.example.com") || strings.Contains(out, "OK   git credential helper configured") {
		t.Fatal(out)
	}
	if strings.Contains(out, broker) || strings.Contains(out, "sensitive-token") {
		t.Fatalf("credential detail leaked: %s", out)
	}
	if err := gitcred.ConfigureHosts([]string{"github.com", "ghe.example.com"}); err != nil {
		t.Fatal(err)
	}
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if strings.Contains(out, "helper not configured for ghe.example.com") || !strings.Contains(out, "OK   git credential helper configured") {
		t.Fatal(out)
	}
	if strings.Contains(out, broker) || strings.Contains(out, "sensitive-token") {
		t.Fatalf("credential detail leaked: %s", out)
	}
}

// Version managers such as mise put gh in a version-specific directory, which
// disappears when that version is uninstalled after an upgrade.
func TestDoctorReportsAndMigrationRepairsMissingHelperRealGH(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	command := NewSetupCmd()
	command.SetArgs([]string{"--native", "--no-shell-rc"})
	captureOnboarding(t, command.Execute)
	target, err := gitcred.Target()
	if err != nil {
		t.Fatal(err)
	}
	removed := filepath.Join(home, "mise", "installs", "gh", "2.100.0", "bin", "gh")
	if _, err := gitcred.Install(target, removed); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "WARN git credential helper gh is missing or not executable: "+removed) {
		t.Fatal(out)
	}
	doctor := NewDoctorCmd()
	doctor.SetArgs([]string{"--fix"})
	captureOnboarding(t, doctor.Execute)
	if got, err := gitcred.RealGH(); err != nil || got != filepath.Join(home, "bin", "gh") {
		t.Fatalf("helper gh = %q, %v; want %q", got, err, filepath.Join(home, "bin", "gh"))
	}
	out = captureOnboarding(t, NewDoctorCmd().Execute)
	if !strings.Contains(out, "OK   git credential helper gh: "+filepath.Join(home, "bin", "gh")) {
		t.Fatal(out)
	}
}

// Beta.1 installs pointed the proxy and helper into a cache directory that
// disappears when gh replaces the extension with a binary install.
func TestDoctorReportsAndMigrationRepairsVanishedExtensionTarget(t *testing.T) {
	if !shims.NativeGHSupported() {
		t.Skip("native routing unsupported")
	}
	home := onboardingHome(t)
	old := filepath.Join(home, "data", "gh", "extensions", "gh-autoaccount", ".autoaccount-cache", "v0.1.0-beta.1-linux-amd64", "gh-autoaccount")
	if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := NewSetupCmd()
	command.SetArgs([]string{"--native", "--no-shell-rc"})
	captureOnboarding(t, command.Execute)
	if _, err := shims.InstallNativeGH(old); err != nil {
		t.Fatal(err)
	}
	if _, err := gitcred.Install(old, filepath.Join(home, "bin", "gh")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(old))); err != nil {
		t.Fatal(err)
	}
	out := captureOnboarding(t, NewDoctorCmd().Execute)
	for _, want := range []string{"WARN required native gh proxy missing, broken, or unmanaged", "WARN git credential helper target is missing or not executable: " + old} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	doctor := NewDoctorCmd()
	doctor.SetArgs([]string{"--fix"})
	captureOnboarding(t, doctor.Execute)
	target, err := gitcred.Target()
	if err != nil || target != exe {
		t.Fatalf("helper target = %q, %v; want %q", target, err, exe)
	}
	link, err := os.Readlink(filepath.Join(home, ".local", "bin", "gh"))
	if err != nil || link != exe {
		t.Fatalf("shim target = %q, %v; want %q", link, err, exe)
	}
}
