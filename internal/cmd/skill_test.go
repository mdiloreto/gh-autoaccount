package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/skills"
)

type skillShortWriter struct{}

func (skillShortWriter) Write(p []byte) (int, error) { return 0, nil }

type skillErrorWriter struct{}

func (skillErrorWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

func TestSkillCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "auth-ran")
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	config := filepath.Join(home, "gh-autoprofile")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(config, "settings.yml")
	if err := os.WriteFile(configFile, []byte("malformed: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetArgs([]string{"skill"})
	root.SetOut(&out)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != skills.Content() || stderr.Len() != 0 {
		t.Fatalf("output mismatch or warning: %q", stderr.String())
	}
	b, err := os.ReadFile(configFile)
	if err != nil || string(b) != "malformed: [\n" {
		t.Fatalf("config changed: %q %v", b, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unexpected auth execution: %v", err)
	}
	out.Reset()
	stderr.Reset()
	root = NewRootCmd()
	root.SetArgs([]string{"skill", "install", "--help"})
	root.SetOut(&out)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil || stderr.Len() != 0 || !strings.Contains(out.String(), "Linux and macOS") {
		t.Fatalf("install help with malformed settings: %v %q", err, stderr.String())
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		out.Reset()
		stderr.Reset()
		root = NewRootCmd()
		root.SetArgs([]string{"skill", "install", "--dir", filepath.Join(home, "malformed-skills")})
		root.SetOut(&out)
		root.SetErr(&stderr)
		if err := root.Execute(); err != nil || stderr.Len() != 0 || !strings.HasPrefix(out.String(), "Installed:") {
			t.Fatalf("install with malformed settings: %v %q", err, stderr.String())
		}
		b, err := os.ReadFile(configFile)
		if err != nil || string(b) != "malformed: [\n" {
			t.Fatalf("config changed: %q %v", b, err)
		}
	}
	if err := os.WriteFile(configFile, []byte("native: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "pins.yml"), []byte("pins:\n  - user: alice\n    dir: /tmp\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	root = NewRootCmd()
	root.SetArgs([]string{"skill"})
	root.SetOut(&out)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil || out.String() != skills.Content() || stderr.Len() != 0 {
		t.Fatalf("drift: %v %q", err, stderr.String())
	}
	for _, w := range []io.Writer{skillShortWriter{}, skillErrorWriter{}} {
		root := NewRootCmd()
		root.SetArgs([]string{"skill"})
		root.SetOut(w)
		root.SetErr(io.Discard)
		if err := root.Execute(); err == nil {
			t.Fatal("expected output error")
		}
	}
	for _, args := range [][]string{{"skill", "extra"}, {"skill", "install"}, {"skill", "install", "--dir", "  "}, {"skill", "install", "--dir", home, "extra"}} {
		root := NewRootCmd()
		root.SetArgs(args)
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		if err := root.Execute(); err == nil {
			t.Fatalf("expected error: %v", args)
		}
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	dir := filepath.Join(home, "skills")
	for _, action := range []string{"Installed:", "Already installed:"} {
		out.Reset()
		root := NewRootCmd()
		root.SetArgs([]string{"skill", "install", "--dir", dir})
		root.SetOut(&out)
		root.SetErr(io.Discard)
		if err := root.Execute(); err != nil || !strings.HasPrefix(out.String(), action) {
			t.Fatalf("install: %q %v", out.String(), err)
		}
	}
	for _, w := range []io.Writer{skillShortWriter{}, skillErrorWriter{}} {
		root = NewRootCmd()
		root.SetArgs([]string{"skill", "install", "--dir", dir})
		root.SetOut(w)
		root.SetErr(io.Discard)
		if err := root.Execute(); err == nil || (w == (skillShortWriter{}) && !errors.Is(err, io.ErrShortWrite)) {
			t.Fatalf("install write error: %v", err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unexpected auth execution: %v", err)
	}
}
