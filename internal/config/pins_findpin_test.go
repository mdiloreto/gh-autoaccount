package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPhysicalPinRouting(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside")
	outside := filepath.Join(root, "outside")
	child := filepath.Join(inside, "child")
	for _, dir := range []string{inside, outside, child} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	inLink := filepath.Join(outside, "in")
	outLink := filepath.Join(inside, "out")
	if err := os.Symlink(child, inLink); err != nil {
		t.Skip(err)
	}
	if err := os.Symlink(outside, outLink); err != nil {
		t.Fatal(err)
	}
	r := &PinRegistry{Pins: []Pin{{User: "root", Dir: string(filepath.Separator)}, {User: "inside", Dir: inside}, {User: "child", Dir: child}}}
	for _, tc := range []struct{ dir, user string }{{inLink, "child"}, {outLink, "root"}, {inside, "inside"}, {outside, "root"}} {
		p, err := r.FindPinStrict(tc.dir)
		if err != nil || p == nil || p.User != tc.user {
			t.Fatalf("%s: %+v %v", tc.dir, p, err)
		}
	}
	r.Pins = append(r.Pins, Pin{Dir: inLink})
	if _, err := r.FindPinStrict(child); err == nil {
		t.Fatal("duplicate physical pin accepted")
	}
}

func TestDeletedPinRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deleted")
	r := &PinRegistry{Pins: []Pin{{Dir: dir}}}
	if !r.RemovePin(dir) {
		t.Fatal("deleted pin not removed")
	}
}

func TestStrictLookupDeletedAndIndependentWorktrees(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "worktree-a"), filepath.Join(root, "worktree-b")
	for _, dir := range []string{a, b} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	r := &PinRegistry{Pins: []Pin{{User: "stale", Dir: filepath.Join(root, "deleted")}, {User: "alice", Dir: a}, {User: "bob", Dir: b}}}
	for _, tc := range []struct{ dir, user string }{{a, "alice"}, {b, "bob"}} {
		pin, err := r.FindPinStrict(tc.dir)
		if err != nil || pin == nil || pin.User != tc.user {
			t.Fatalf("%s: %+v %v", tc.dir, pin, err)
		}
	}
	if !pathContains(filepath.Join(root, "deleted"), filepath.Join(root, "deleted", "child")) {
		t.Fatal("stale ancestor should be relevant")
	}
}

func TestStrictAddRejectsDuplicateCanonicalTargets(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skip(err)
	}
	r := &PinRegistry{Pins: []Pin{{Dir: dir, User: "a"}, {Dir: alias, User: "b"}}}
	if err := r.AddPinStrict(Pin{Dir: dir, User: "c"}); err == nil {
		t.Fatal("accepted duplicate targets")
	}
	if r.Pins[0].User != "a" || r.Pins[1].User != "b" {
		t.Fatal("modified registry on error")
	}
}

func TestStrictLookupSelectedPinDisappears(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "pin")
	query := filepath.Join(parent, "child")
	if err := os.MkdirAll(query, 0700); err != nil {
		t.Fatal(err)
	}
	r := &PinRegistry{Pins: []Pin{{Dir: parent, User: "alice"}}}
	canonical := func(path string) (string, error) {
		if path == parent {
			if err := os.RemoveAll(parent); err != nil {
				t.Fatal(err)
			}
		}
		return CanonicalDir(path)
	}
	if _, err := r.findPinStrict(query, canonical); err == nil {
		t.Fatal("selected pin disappeared during lookup")
	}
}

func TestSameUserDifferentHostsAndLegacyDefault(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	r := &PinRegistry{}
	for _, pin := range []Pin{{User: "alice", Dir: a}, {User: "alice", Host: "GIT.Example.COM", Dir: b}} {
		if err := r.AddPinStrict(pin); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.Pins) != 2 || r.Pins[0].Host != "" || r.Pins[1].Host != "git.example.com" {
		t.Fatalf("pins: %+v", r.Pins)
	}
	if host, err := NormalizeHost(r.Pins[0].Host); err != nil || host != "github.com" {
		t.Fatalf("legacy host: %s %v", host, err)
	}
}

func TestNormalizeHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{{"", "github.com"}, {"GHE.Example.COM", "ghe.example.com"}, {"https://example.com", ""}, {"example.com:443", ""}, {"example.com/", ""}, {"a b", ""}, {"-a.com", ""}, {"a..com", ""}} {
		got, err := NormalizeHost(tc.in)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("%q: %q %v", tc.in, got, err)
		}
	}
}

func TestFindPin_ExactMatch(t *testing.T) {
	r := &PinRegistry{Pins: []Pin{{User: "alice", Dir: "/tmp/work"}}}
	pin := r.FindPin("/tmp/work")
	if pin == nil || pin.User != "alice" {
		t.Fatalf("FindPin exact match failed: %+v", pin)
	}
}

func TestFindPin_ChildDirectoryUsesAncestorPin(t *testing.T) {
	r := &PinRegistry{Pins: []Pin{{User: "alice", Dir: "/tmp/work"}}}
	pin := r.FindPin("/tmp/work/repo")
	if pin == nil || pin.User != "alice" {
		t.Fatalf("FindPin child match failed: %+v", pin)
	}
}

func TestFindPin_PrefersMostSpecificAncestor(t *testing.T) {
	r := &PinRegistry{Pins: []Pin{
		{User: "root", Dir: "/tmp/work"},
		{User: "repo", Dir: "/tmp/work/repo"},
	}}
	pin := r.FindPin("/tmp/work/repo/subdir")
	if pin == nil || pin.User != "repo" {
		t.Fatalf("FindPin should prefer nearest ancestor, got %+v", pin)
	}
}

func TestFindPin_DoesNotMatchSiblingPrefix(t *testing.T) {
	r := &PinRegistry{Pins: []Pin{{User: "alice", Dir: "/tmp/work"}}}
	pin := r.FindPin("/tmp/workshop")
	if pin != nil {
		t.Fatalf("FindPin should not match sibling prefix, got %+v", pin)
	}
}
