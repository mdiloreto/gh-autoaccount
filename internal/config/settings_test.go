package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSettings_UnknownFieldRejected(t *testing.T) {
	// Same fail-closed rule as pins.yml: a typo'd setting key must error
	// loudly instead of silently dropping configuration.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, "gh-autoprofile")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	typo := "git_identityy: true\n"
	if err := os.WriteFile(filepath.Join(configDir, "settings.yml"), []byte(typo), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadSettings()
	if err == nil {
		t.Fatal("expected unknown-field error, got nil")
	}
	if !strings.Contains(err.Error(), "git_identityy") {
		t.Fatalf("error should name the unknown key: %v", err)
	}
}

func TestLoadSettings_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	if err := SaveSettings(Settings{GitIdentity: true, ExternalShell: true, Native: true}); err != nil {
		t.Fatalf("SaveSettings failed: %v", err)
	}
	loaded, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings failed: %v", err)
	}
	if !loaded.GitIdentity || !loaded.ExternalShell || !loaded.Native {
		t.Fatalf("round-trip mismatch: %+v", loaded)
	}
	loaded.Native = false
	if err := SaveSettings(loaded); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tmpDir, "gh-autoprofile", "settings.yml"))
	if err != nil || strings.Contains(string(data), "native:") {
		t.Fatalf("false native setting not omitted: %q, %v", data, err)
	}
	loaded, err = LoadSettings()
	if err != nil || loaded.Native || !loaded.ExternalShell || !loaded.GitIdentity {
		t.Fatalf("legacy settings mismatch: %+v, %v", loaded, err)
	}
}
