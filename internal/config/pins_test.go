package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinRegistry_AddPin(t *testing.T) {
	reg := &PinRegistry{}

	// Add first pin
	reg.AddPin(Pin{User: "alice", Dir: "/tmp/test-a"})
	if len(reg.Pins) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(reg.Pins))
	}
	if reg.Pins[0].User != "alice" {
		t.Errorf("expected user 'alice', got '%s'", reg.Pins[0].User)
	}

	// Add second pin
	reg.AddPin(Pin{User: "bob", Dir: "/tmp/test-b"})
	if len(reg.Pins) != 2 {
		t.Fatalf("expected 2 pins, got %d", len(reg.Pins))
	}

	// Update existing pin (same dir)
	reg.AddPin(Pin{User: "charlie", Dir: "/tmp/test-a"})
	if len(reg.Pins) != 2 {
		t.Fatalf("expected 2 pins after update, got %d", len(reg.Pins))
	}
	if reg.Pins[0].User != "charlie" {
		t.Errorf("expected user 'charlie' after update, got '%s'", reg.Pins[0].User)
	}
}

func TestPinRegistry_FindPin(t *testing.T) {
	reg := &PinRegistry{
		Pins: []Pin{
			{User: "alice", Dir: "/tmp/test-a"},
			{User: "bob", Dir: "/tmp/test-b", GitEmail: "bob@example.test"},
		},
	}

	// Find existing
	pin := reg.FindPin("/tmp/test-b")
	if pin == nil {
		t.Fatal("expected to find pin for /tmp/test-b")
	}
	if pin.User != "bob" {
		t.Errorf("expected user 'bob', got '%s'", pin.User)
	}
	if pin.GitEmail != "bob@example.test" {
		t.Errorf("expected email 'bob@example.test', got '%s'", pin.GitEmail)
	}

	// Find non-existing
	pin = reg.FindPin("/tmp/nonexistent")
	if pin != nil {
		t.Error("expected nil for non-existing directory")
	}
}

func TestPinRegistry_FindPinForDir(t *testing.T) {
	reg := &PinRegistry{
		Pins: []Pin{
			{User: "alice", Dir: "/tmp/work"},
			{User: "bob", Dir: "/tmp/work/personal"},
		},
	}

	pin := reg.FindPinForDir("/tmp/work/personal/project")
	if pin == nil {
		t.Fatal("expected inherited pin for nested directory")
	}
	if pin.User != "bob" {
		t.Fatalf("expected closest inherited pin 'bob', got %q", pin.User)
	}

	pin = reg.FindPinForDir("/tmp/work/other")
	if pin == nil {
		t.Fatal("expected parent pin for sibling directory")
	}
	if pin.User != "alice" {
		t.Fatalf("expected parent pin 'alice', got %q", pin.User)
	}

	pin = reg.FindPinForDir("/tmp/unpinned")
	if pin != nil {
		t.Fatal("expected nil for directory outside any pin")
	}
}

func TestPinRegistry_FindPinByPath(t *testing.T) {
	reg := &PinRegistry{
		Pins: []Pin{
			{User: "alice", Dir: "/home/user/personal"},
			{User: "bob-work", Dir: "/home/user/work"},
			{User: "client-a", Dir: "/home/user/work/client-a"},
		},
	}

	tests := []struct {
		name     string
		dir      string
		wantUser string
		wantNil  bool
	}{
		{
			name:     "exact match",
			dir:      "/home/user/work",
			wantUser: "bob-work",
		},
		{
			name:     "child directory matches parent pin",
			dir:      "/home/user/work/some-repo",
			wantUser: "bob-work",
		},
		{
			name:     "deep child matches parent pin",
			dir:      "/home/user/personal/projects/my-app/src",
			wantUser: "alice",
		},
		{
			name:     "deepest pin wins (nested pins)",
			dir:      "/home/user/work/client-a/repo",
			wantUser: "client-a",
		},
		{
			name:     "exact match on nested pin",
			dir:      "/home/user/work/client-a",
			wantUser: "client-a",
		},
		{
			name:    "no match - unrelated path",
			dir:     "/home/user/other",
			wantNil: true,
		},
		{
			name:    "no match - root",
			dir:     "/",
			wantNil: true,
		},
		{
			name:    "no match - partial prefix that is not a path boundary",
			dir:     "/home/user/workspaces",
			wantNil: true,
		},
		{
			name:     "child of personal",
			dir:      "/home/user/personal/dotfiles",
			wantUser: "alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pin := reg.FindPinByPath(tt.dir)
			if tt.wantNil {
				if pin != nil {
					t.Errorf("expected nil, got pin for user %q", pin.User)
				}
				return
			}
			if pin == nil {
				t.Fatalf("expected pin for user %q, got nil", tt.wantUser)
			}
			if pin.User != tt.wantUser {
				t.Errorf("expected user %q, got %q", tt.wantUser, pin.User)
			}
		})
	}
}

func TestPinRegistry_RemovePin(t *testing.T) {
	reg := &PinRegistry{
		Pins: []Pin{
			{User: "alice", Dir: "/tmp/test-a"},
			{User: "bob", Dir: "/tmp/test-b"},
			{User: "charlie", Dir: "/tmp/test-c"},
		},
	}

	// Remove middle pin
	removed := reg.RemovePin("/tmp/test-b")
	if !removed {
		t.Error("expected RemovePin to return true")
	}
	if len(reg.Pins) != 2 {
		t.Fatalf("expected 2 pins after removal, got %d", len(reg.Pins))
	}
	if reg.Pins[0].User != "alice" || reg.Pins[1].User != "charlie" {
		t.Error("wrong pins remaining after removal")
	}

	// Remove non-existing
	removed = reg.RemovePin("/tmp/nonexistent")
	if removed {
		t.Error("expected RemovePin to return false for non-existing")
	}
}

func TestSaveAndLoadPins(t *testing.T) {
	// Use a temp dir for config
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Save
	registry := &PinRegistry{
		Pins: []Pin{
			{User: "alice", Dir: "/tmp/test-a", GitEmail: "alice@example.test", GitName: "Alice"},
			{User: "bob", Dir: "/tmp/test-b", SSHKey: "/home/bob/.ssh/id_ed25519"},
		},
	}
	if err := SavePins(registry); err != nil {
		t.Fatalf("SavePins failed: %v", err)
	}

	// Verify file exists
	pinsFile := filepath.Join(tmpDir, "gh-autoprofile", "pins.yml")
	if _, err := os.Stat(pinsFile); err != nil {
		t.Fatalf("pins file not created: %v", err)
	}

	// Load
	loaded, err := LoadPins()
	if err != nil {
		t.Fatalf("LoadPins failed: %v", err)
	}
	if len(loaded.Pins) != 2 {
		t.Fatalf("expected 2 pins, got %d", len(loaded.Pins))
	}
	if loaded.Pins[0].User != "alice" || loaded.Pins[0].GitEmail != "alice@example.test" {
		t.Errorf("pin 0 mismatch: %+v", loaded.Pins[0])
	}
	if loaded.Pins[1].User != "bob" || loaded.Pins[1].SSHKey != "/home/bob/.ssh/id_ed25519" {
		t.Errorf("pin 1 mismatch: %+v", loaded.Pins[1])
	}
}

func TestLoadPins_EmptyWhenNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	registry, err := LoadPins()
	if err != nil {
		t.Fatalf("LoadPins failed: %v", err)
	}
	if len(registry.Pins) != 0 {
		t.Errorf("expected empty registry, got %d pins", len(registry.Pins))
	}
}

func TestEffectiveMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     PinMode
		expected PinMode
	}{
		{"empty defaults to wrapper", "", ModeWrapper},
		{"explicit wrapper", ModeWrapper, ModeWrapper},
		{"explicit export", ModeExport, ModeExport},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pin := &Pin{User: "alice", Dir: "/tmp/test", Mode: tt.mode}
			if got := pin.EffectiveMode(); got != tt.expected {
				t.Errorf("EffectiveMode() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestSaveAndLoadPins_WithMode(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	registry := &PinRegistry{
		Pins: []Pin{
			{User: "alice", Dir: "/tmp/test-a", Mode: ModeWrapper},
			{User: "bob", Dir: "/tmp/test-b", Mode: ModeExport, GitEmail: "bob@example.test"},
			{User: "charlie", Dir: "/tmp/test-c"}, // no mode — should default to wrapper
		},
	}
	if err := SavePins(registry); err != nil {
		t.Fatalf("SavePins failed: %v", err)
	}

	// Verify file permissions
	pinsFile := filepath.Join(tmpDir, "gh-autoprofile", "pins.yml")
	info, err := os.Stat(pinsFile)
	if err != nil {
		t.Fatalf("pins file not created: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("pins file permissions = %o, want 0600", perm)
	}

	// Verify config dir permissions
	dirInfo, err := os.Stat(filepath.Join(tmpDir, "gh-autoprofile"))
	if err != nil {
		t.Fatalf("config dir not created: %v", err)
	}
	dirPerm := dirInfo.Mode().Perm()
	if dirPerm != 0700 {
		t.Errorf("config dir permissions = %o, want 0700", dirPerm)
	}

	// Load and verify
	loaded, err := LoadPins()
	if err != nil {
		t.Fatalf("LoadPins failed: %v", err)
	}
	if len(loaded.Pins) != 3 {
		t.Fatalf("expected 3 pins, got %d", len(loaded.Pins))
	}

	// Explicit wrapper
	if loaded.Pins[0].Mode != ModeWrapper {
		t.Errorf("pin 0 mode = %q, want %q", loaded.Pins[0].Mode, ModeWrapper)
	}
	if loaded.Pins[0].EffectiveMode() != ModeWrapper {
		t.Errorf("pin 0 EffectiveMode() = %q, want %q", loaded.Pins[0].EffectiveMode(), ModeWrapper)
	}

	// Explicit export
	if loaded.Pins[1].Mode != ModeExport {
		t.Errorf("pin 1 mode = %q, want %q", loaded.Pins[1].Mode, ModeExport)
	}
	if loaded.Pins[1].EffectiveMode() != ModeExport {
		t.Errorf("pin 1 EffectiveMode() = %q, want %q", loaded.Pins[1].EffectiveMode(), ModeExport)
	}

	// Empty mode defaults to wrapper
	if loaded.Pins[2].Mode != "" {
		t.Errorf("pin 2 mode = %q, want empty", loaded.Pins[2].Mode)
	}
	if loaded.Pins[2].EffectiveMode() != ModeWrapper {
		t.Errorf("pin 2 EffectiveMode() = %q, want %q", loaded.Pins[2].EffectiveMode(), ModeWrapper)
	}
}

func TestAddPin_PreservesMode(t *testing.T) {
	reg := &PinRegistry{}

	// Add pin with export mode
	reg.AddPin(Pin{User: "alice", Dir: "/tmp/test-a", Mode: ModeExport})
	if reg.Pins[0].Mode != ModeExport {
		t.Errorf("expected mode %q, got %q", ModeExport, reg.Pins[0].Mode)
	}

	// Update same dir — mode should be updated too
	reg.AddPin(Pin{User: "alice", Dir: "/tmp/test-a", Mode: ModeWrapper})
	if len(reg.Pins) != 1 {
		t.Fatalf("expected 1 pin after update, got %d", len(reg.Pins))
	}
	if reg.Pins[0].Mode != ModeWrapper {
		t.Errorf("expected mode %q after update, got %q", ModeWrapper, reg.Pins[0].Mode)
	}
}

func TestLoadPins_UnknownFieldRejected(t *testing.T) {
	// A hand-edit typo must fail closed with the offending key named, not
	// silently drop account scoping.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, "gh-autoprofile")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	typo := "pins:\n  - user: alice\n    dir: /tmp/test-a\n    mode: wrapper\n    ssh_keyy: /home/alice/.ssh/id_work\n"
	if err := os.WriteFile(filepath.Join(configDir, "pins.yml"), []byte(typo), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPins()
	if err == nil {
		t.Fatal("expected unknown-field error, got nil (silent fail-open to default account)")
	}
	if !strings.Contains(err.Error(), "ssh_keyy") {
		t.Fatalf("error should name the unknown key: %v", err)
	}
}

func TestLoadPins_EmptyAndCommentOnlyFilesOK(t *testing.T) {
	// Regression guard for the strict-decoder change: empty and comment-only
	// files must keep loading as an empty registry.
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, "gh-autoprofile")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}

	for name, content := range map[string]string{
		"empty":           "",
		"comment-only":    "# gh-autoprofile pins\n",
		"whitespace-only": "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(configDir, "pins.yml"), []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			registry, err := LoadPins()
			if err != nil {
				t.Fatalf("LoadPins failed on %s file: %v", name, err)
			}
			if len(registry.Pins) != 0 {
				t.Fatalf("expected empty registry for %s file, got %d pins", name, len(registry.Pins))
			}
		})
	}
}
