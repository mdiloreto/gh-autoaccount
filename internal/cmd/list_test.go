package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestListJSONEmpty(t *testing.T) {
	diagnosticsHome(t)
	forbidDiagnosticHTTP(t)
	for _, command := range []string{"list", "ls"} {
		stdout, stderr, err := executeDiagnostics(command, "--json")
		if err != nil || stderr != "" {
			t.Fatalf("list failed: %v, %s", err, stderr)
		}
		var actual map[string]any
		decodeDiagnosticJSON(t, stdout, &actual)
		want := map[string]any{"schema_version": float64(1), "pins": []any{}}
		if !reflect.DeepEqual(actual, want) {
			t.Fatalf("expected empty versioned array, got %s", stdout)
		}
	}
}

func TestListJSONPins(t *testing.T) {
	home := diagnosticsHome(t)
	registry := &config.PinRegistry{Pins: []config.Pin{
		{User: "nested", Host: "GHES.EXAMPLE", TokenCommand: "/private/broker", Dir: filepath.Join(home, "parent", "nested"), Mode: config.ModeExport, GitName: "Nested User", GitEmail: "nested@example.test", SSHKey: "/fixture/key"},
		{User: "parent", Dir: filepath.Join(home, "parent")},
	}}
	if err := config.SavePins(registry); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeDiagnostics("list", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("list failed: %v, %s", err, stderr)
	}
	var actual map[string]any
	decodeDiagnosticJSON(t, stdout, &actual)
	want := map[string]any{
		"schema_version": float64(1),
		"pins": []any{
			map[string]any{"user": "nested", "host": "ghes.example", "token_command_configured": true, "directory": registry.Pins[0].Dir, "mode": "export", "git_name": "Nested User", "git_email": "nested@example.test", "ssh_key": "/fixture/key"},
			map[string]any{"user": "parent", "host": "github.com", "token_command_configured": false, "directory": registry.Pins[1].Dir, "mode": "wrapper"},
		},
	}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("wrong pin metadata/order: %s", stdout)
	}
}

func TestListTextPreserved(t *testing.T) {
	home := diagnosticsHome(t)
	stdout, _, err := executeDiagnostics("list")
	if err != nil || stdout != "No pinned directories.\nPin one with: gh autoaccount pin <username> --dir <path>\n" {
		t.Fatalf("empty list text changed: %q, %v", stdout, err)
	}
	t.Chdir(home)
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{Dir: home, User: "alice"}}}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = executeDiagnostics("list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DIRECTORY", "ACCOUNT", "MODE", "GIT EMAIL", "GIT NAME", "SSH KEY", "* " + home, "alice", "wrapper", "1 pin(s) total. (* = selected pin)"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("missing %q in table: %s", want, stdout)
		}
	}
}

func TestListHighlightsCanonicalSelectedPin(t *testing.T) {
	home := diagnosticsHome(t)
	physical := filepath.Join(home, "physical")
	child := filepath.Join(physical, "child")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias")
	if err := os.Symlink(physical, alias); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(home, "deleted-unrelated")
	if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{
		{Dir: alias, User: "alice"},
		{Dir: missing, User: "legacy"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	stdout, _, err := executeDiagnostics("list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "* "+alias) || !strings.Contains(stdout, missing) {
		t.Fatalf("expected canonical pin highlighted and unrelated deleted pin listed: %s", stdout)
	}
}

func TestListJSONRegistryFailureHasNoOutput(t *testing.T) {
	diagnosticsHome(t)
	if err := config.SavePins(&config.PinRegistry{}); err != nil {
		t.Fatal(err)
	}
	path, err := config.PinsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(diagnosticStoredToken+": invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeDiagnostics("list", "--json")
	if err == nil || stdout != "" || stderr != "" {
		t.Fatalf("expected silent stdout on fatal error, got %q, %q, %v", stdout, stderr, err)
	}
	assertDiagnosticRedacted(t, err.Error())
	if !strings.Contains(err.Error(), "pins.yml") || !strings.Contains(err.Error(), "field names") {
		t.Fatalf("error lost actionable registry repair guidance: %v", err)
	}
}

func TestListDoesNotRequireValidSettings(t *testing.T) {
	diagnosticsHome(t)
	if err := config.SavePins(&config.PinRegistry{}); err != nil {
		t.Fatal(err)
	}
	configDir, err := config.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.yml"), []byte(diagnosticStoredToken+": invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeDiagnostics("list")
	if err != nil || !strings.Contains(stdout, "No pinned directories.") || !strings.Contains(stderr, "settings.yml") {
		t.Fatalf("list should retain its settings warning without failing: %q, %q, %v", stdout, stderr, err)
	}
	assertDiagnosticRedacted(t, stdout+stderr)
	stdout, stderr, err = executeDiagnostics("list", "--json")
	if err != nil || stderr != "" || stdout != "{\"schema_version\":1,\"pins\":[]}\n" {
		t.Fatalf("JSON list must not depend on settings: %q, %q, %v", stdout, stderr, err)
	}
}
