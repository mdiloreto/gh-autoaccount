package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestDefaultCommandPreservesSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gh := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\n[ \"$4\" = alice ] || exit 1\nprintf 'fake-token\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	if err := config.SaveSettings(config.Settings{Native: true, GitIdentity: true, ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		c := NewDefaultCmd()
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetArgs(args)
		err := c.Execute()
		return out.String(), err
	}
	if _, err := run("alice"); err != nil {
		t.Fatal(err)
	}
	s, err := config.LoadSettings()
	if err != nil || s.DefaultUser != "alice" || !s.Native || !s.GitIdentity || !s.ExternalShell {
		t.Fatalf("settings: %+v %v", s, err)
	}
	if out, err := run(); err != nil || !strings.Contains(out, "alice") {
		t.Fatalf("show: %q %v", out, err)
	}
	if _, err := run("--unset", "alice"); err == nil {
		t.Fatal("accepted conflicting arguments")
	}
	if _, err := run(""); err == nil {
		t.Fatal("accepted empty user")
	}
	if _, err := run("missing"); err == nil {
		t.Fatal("accepted missing credential")
	}
	if _, err := run("--unset"); err != nil {
		t.Fatal(err)
	}
	s, err = config.LoadSettings()
	if err != nil || s.DefaultUser != "" || !s.Native || !s.GitIdentity || !s.ExternalShell {
		t.Fatalf("unset settings: %+v %v", s, err)
	}
}

func TestDefaultMutationFailureAndRecovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	logPath := filepath.Join(home, "auth-calls")
	t.Setenv("TEST_AUTH_LOG", logPath)
	gh := filepath.Join(home, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\nprintf 'called\\n' >> \"$TEST_AUTH_LOG\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice", Native: true, GitIdentity: true, ExternalShell: true}); err != nil {
		t.Fatal(err)
	}
	dir, err := config.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, "settings.yml")
	before, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		cmd := NewDefaultCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	if err := run("bob"); err == nil {
		t.Fatal("set accepted an unavailable credential")
	}
	after, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed set changed settings: %q, %v", after, err)
	}
	if err := os.Remove(logPath); err != nil {
		t.Fatal(err)
	}
	if err := run("--unset"); err != nil {
		t.Fatalf("unset unavailable default: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("unset attempted credential lookup: %v", err)
	}
	settings, err := config.LoadSettings()
	if err != nil || settings.DefaultUser != "" || !settings.Native || !settings.GitIdentity || !settings.ExternalShell {
		t.Fatalf("unset did not preserve flags: %+v, %v", settings, err)
	}
	malformed := []byte("native: true\nunknown_field: retained\n")
	if err := os.WriteFile(settingsPath, malformed, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"bob"}, {"--unset"}} {
		if err := run(args...); err == nil {
			t.Fatalf("%v accepted malformed settings", args)
		}
		got, err := os.ReadFile(settingsPath)
		if err != nil || !bytes.Equal(got, malformed) {
			t.Fatalf("%v changed malformed settings: %q, %v", args, got, err)
		}
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("malformed settings triggered credential lookup: %v", err)
	}
}
