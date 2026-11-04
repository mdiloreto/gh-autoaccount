package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func TestReadCredentialRequest(t *testing.T) {
	content := "protocol=https\nhost=github.com\npath=foo/bar.git\n\n"
	file, err := createTempReaderFile(t, content)
	if err != nil {
		t.Fatalf("temp file: %v", err)
	}
	req, err := readCredentialRequest(file)
	if err != nil {
		t.Fatalf("readCredentialRequest failed: %v", err)
	}
	if req.protocol != "https" || req.host != "github.com" || req.path != "foo/bar.git" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func createTempReaderFile(t *testing.T, content string) (*os.File, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "cred-*")
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(content); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	return f, nil
}

// runCredentialGet drives the helper seam and returns what Git would read.
func runCredentialGet(t *testing.T, request string) (string, error) {
	t.Helper()
	in, err := createTempReaderFile(t, request)
	if err != nil {
		t.Fatal(err)
	}
	req, err := readCredentialRequest(in)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := handleGitCredentialGet(req)
	os.Stdout = old
	w.Close()
	out, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out), runErr
}

// credentialFixture isolates config and gives a fake gh that prints tok-<user>.
func credentialFixture(t *testing.T, pinsYAML string) (unpinned, pinned string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	unpinned, pinned = filepath.Join(root, "unpinned"), filepath.Join(root, "work")
	for _, dir := range []string{unpinned, pinned, filepath.Join(root, "config", "gh-autoprofile")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gh := filepath.Join(root, "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\n[ \"$1 $2 $3\" = 'auth token --user' ] && printf 'tok-%s\\n' \"$4\" || exit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", gh)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", gh)
	pins := strings.ReplaceAll(pinsYAML, "{{work}}", pinned)
	if err := os.WriteFile(filepath.Join(root, "config", "gh-autoprofile", "pins.yml"), []byte(pins), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(config.Settings{DefaultUser: "alice"}); err != nil {
		t.Fatal(err)
	}
	return unpinned, pinned
}

func TestCredentialHelperOwnerPinWinsOverCwdSelection(t *testing.T) {
	unpinned, _ := credentialFixture(t, "pins:\n  - user: bob\n    owner: work-org\n")
	t.Chdir(unpinned)
	out, err := runCredentialGet(t, "protocol=https\nhost=github.com\npath=work-org/x.git\n\n")
	if err != nil {
		t.Fatalf("helper failed: %v (output %q)", err, out)
	}
	if want := "username=x-access-token\npassword=tok-bob\n\n"; out != want {
		t.Fatalf("credential = %q, want %q", out, want)
	}
}
