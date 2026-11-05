package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestCurrentPrintsSelectionWithoutResolvingCredentials(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GH_AUTOACCOUNT_USER", "")
	// Any credential lookup would run gh; this one records that it ran.
	ran := filepath.Join(root, "gh-ran")
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\n: > '"+ran+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", gh)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)

	work, unpinned, checkout := filepath.Join(root, "work"), filepath.Join(root, "unpinned"), filepath.Join(root, "checkout")
	for _, dir := range []string{work, unpinned, checkout} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q", checkout}, {"-C", checkout, "remote", "add", "origin", "https://github.com/work-org/x.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{
		{User: "carol", Dir: work, Mode: config.ModeWrapper},
		{User: "bob", Owner: "work-org", Mode: config.ModeWrapper},
	}}); err != nil {
		t.Fatal(err)
	}

	current := func(dir string) string {
		t.Helper()
		t.Chdir(dir)
		var out bytes.Buffer
		command := NewCurrentCmd()
		command.SetOut(&out)
		command.SetArgs(nil)
		if err := command.Execute(); err != nil {
			t.Fatalf("current in %s: %v", dir, err)
		}
		return out.String()
	}
	if got := current(work); got != "carol\n" {
		t.Fatalf("directory pin: %q", got)
	}
	if got := current(checkout); got != "bob\n" {
		t.Fatalf("owner pin from the checkout's remote: %q", got)
	}
	if got := current(unpinned); got != "" {
		t.Fatalf("no selection must print nothing: %q", got)
	}
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	if got := current(unpinned); got != "alice\n" {
		t.Fatalf("default: %q", got)
	}
	t.Setenv("GH_AUTOACCOUNT_USER", "erin")
	if got := current(work); got != "erin\n" {
		t.Fatalf("session override: %q", got)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatalf("current resolved a credential: %v", err)
	}
}
