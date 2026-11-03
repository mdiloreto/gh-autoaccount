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
	if !strings.Contains(out, "OK   git credential helper installed") || !strings.Contains(out, "helper target executable is not verified") {
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
