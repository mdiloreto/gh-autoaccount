package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePinsFile(t *testing.T, content string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := PinsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPinsOwnerValidation(t *testing.T) {
	for _, tc := range []struct{ name, yaml, wantErr string }{
		{"legacy directory registry", "pins:\n  - user: alice\n    dir: /tmp/a\n  - user: bob\n    dir: /tmp/b\n    mode: export\n", ""},
		{"owner pin", "pins:\n  - user: bob\n    owner: work-org\n", ""},
		{"owner and directory pins", "pins:\n  - user: alice\n    dir: /tmp/a\n  - user: bob\n    owner: work-org\n    host: GHE.example.test\n    mode: wrapper\n", ""},
		{"same owner on different hosts", "pins:\n  - user: a\n    owner: org\n  - user: b\n    owner: org\n    host: ghe.example.test\n", ""},
		{"neither dir nor owner", "pins:\n  - user: alice\n", "exactly one of dir or owner"},
		{"both dir and owner", "pins:\n  - user: alice\n    dir: /tmp/a\n    owner: org\n", "exactly one of dir or owner"},
		{"invalid owner", "pins:\n  - user: alice\n    owner: -bad-\n", "invalid owner"},
		{"owner with slash", "pins:\n  - user: alice\n    owner: org/repo\n", "invalid owner"},
		{"export mode", "pins:\n  - user: alice\n    owner: org\n    mode: export\n", "wrapper mode only"},
		{"invalid host", "pins:\n  - user: alice\n    owner: org\n    host: not a host\n", "invalid hostname"},
		{"duplicate owner", "pins:\n  - user: a\n    owner: org\n  - user: b\n    owner: org\n", "duplicate owner pin"},
		{"duplicate owner case and host default", "pins:\n  - user: a\n    owner: Org\n  - user: b\n    owner: oRG\n    host: GitHub.com\n", "duplicate owner pin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writePinsFile(t, tc.yaml)
			_, err := LoadPins()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestOwnerPinRoundTripOmitsDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	registry := &PinRegistry{Pins: []Pin{{User: "bob", Owner: "work-org", Mode: ModeWrapper}}}
	if err := SavePins(registry); err != nil {
		t.Fatal(err)
	}
	path, _ := PinsFilePath()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "dir:") || !strings.Contains(string(data), "owner: work-org") {
		t.Fatalf("unexpected pins file:\n%s", data)
	}
	loaded, err := LoadPins()
	if err != nil || len(loaded.Pins) != 1 || loaded.Pins[0].Owner != "work-org" {
		t.Fatalf("round trip: %+v %v", loaded, err)
	}
}

func TestFindOwnerPin(t *testing.T) {
	registry := &PinRegistry{Pins: []Pin{
		{User: "alice", Dir: "/tmp/a"},
		{User: "bob", Owner: "Work-Org"},
		{User: "carol", Owner: "work-org", Host: "ghe.example.test"},
	}}
	for _, tc := range []struct{ host, owner, want string }{
		{"github.com", "work-org", "bob"},
		{"", "WORK-ORG", "bob"},
		{"GitHub.com", "Work-Org", "bob"},
		{"ghe.example.test", "work-org", "carol"},
		{"other.example.test", "work-org", ""},
		{"github.com", "other-org", ""},
		{"github.com", "", ""},
		{"not a host", "work-org", ""},
	} {
		got := registry.FindOwnerPin(tc.host, tc.owner)
		if tc.want == "" && got != nil || tc.want != "" && (got == nil || got.User != tc.want) {
			t.Errorf("FindOwnerPin(%q, %q) = %+v, want user %q", tc.host, tc.owner, got, tc.want)
		}
	}
	if !registry.HasOwnerPins() || (&PinRegistry{Pins: registry.Pins[:1]}).HasOwnerPins() {
		t.Error("HasOwnerPins mismatch")
	}
}

func TestAddAndRemoveOwnerPin(t *testing.T) {
	registry := &PinRegistry{}
	if err := registry.AddOwnerPin(Pin{User: "bob", Owner: "Work-Org", Host: "GITHUB.COM"}); err != nil {
		t.Fatal(err)
	}
	if registry.Pins[0].Host != "github.com" {
		t.Fatalf("host not normalized: %q", registry.Pins[0].Host)
	}
	if err := registry.AddOwnerPin(Pin{User: "carol", Owner: "work-org"}); err != nil || len(registry.Pins) != 1 || registry.Pins[0].User != "carol" {
		t.Fatalf("same (host, owner) must replace: %+v %v", registry.Pins, err)
	}
	for _, bad := range []Pin{
		{User: "x", Owner: "org", Dir: "/tmp/a"},
		{User: "x", Owner: "org", Mode: ModeExport},
		{User: "x", Owner: "bad owner"},
		{User: "x", Owner: "org", Host: "bad host"},
	} {
		if err := registry.AddOwnerPin(bad); err == nil {
			t.Errorf("AddOwnerPin accepted %+v", bad)
		}
	}
	if registry.RemoveOwnerPin("ghe.example.test", "work-org") || !registry.RemoveOwnerPin("", "WORK-ORG") || len(registry.Pins) != 0 {
		t.Fatalf("remove mismatch: %+v", registry.Pins)
	}
}

// Owner pins have no directory; directory helpers must never treat the empty
// Dir as the current directory.
func TestOwnerPinsAreInvisibleToDirectoryLookups(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	registry := &PinRegistry{Pins: []Pin{{User: "bob", Owner: "work-org"}}}
	if pin, err := registry.FindPinStrict(dir); pin != nil || err != nil {
		t.Fatalf("FindPinStrict selected an owner pin: %+v %v", pin, err)
	}
	if registry.FindPin(dir) != nil || registry.FindPinByPath(dir) != nil {
		t.Fatal("legacy lookups selected an owner pin")
	}
	if registry.RemovePin(dir) || registry.RemovePin(".") || len(registry.Pins) != 1 {
		t.Fatal("RemovePin removed an owner pin")
	}
	if err := registry.AddPinStrict(Pin{User: "alice", Dir: dir}); err != nil || len(registry.Pins) != 2 || registry.Pins[0].User != "bob" {
		t.Fatalf("AddPinStrict replaced an owner pin: %+v %v", registry.Pins, err)
	}
}
