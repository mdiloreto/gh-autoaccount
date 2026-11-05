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

func TestCredentialHelperNamesMissingRecordedGH(t *testing.T) {
	_, pinned := credentialFixture(t, "pins:\n  - user: carol\n    dir: \"{{work}}\"\n")
	missing := filepath.Join(t.TempDir(), "installs", "gh", "2.100.0", "bin", "gh")
	t.Setenv("GH_PATH", missing)
	t.Setenv("GH_AUTOPROFILE_REAL_GH", missing)
	t.Chdir(pinned)
	out, err := runCredentialGet(t, "protocol=https\nhost=github.com\n\n")
	if out != "quit=true\n\n" {
		t.Fatalf("output = %q, want quit", out)
	}
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "gh autoaccount setup --migrate") {
		t.Fatalf("error does not name the missing gh and the fix: %v", err)
	}
}

func TestCredentialHelperSelection(t *testing.T) {
	const pins = `pins:
  - user: bob
    owner: work-org
  - user: dana
    owner: ghe-org
    host: ghe.example.test
  - user: carol
    dir: "{{work}}"
`
	for _, tc := range []struct {
		name    string
		inPin   bool // run from the carol-pinned directory
		request string
		want    string // user whose token is expected
		wantErr bool
	}{
		{name: "no path keeps cwd default", request: "protocol=https\nhost=github.com\n\n", want: "alice"},
		{name: "no path keeps cwd dir pin", inPin: true, request: "protocol=https\nhost=github.com\n\n", want: "carol"},
		{name: "owner path from unpinned cwd", request: "protocol=https\nhost=github.com\npath=work-org/x.git\n\n", want: "bob"},
		{name: "owner path without .git", request: "protocol=https\nhost=github.com\npath=work-org/x\n\n", want: "bob"},
		{name: "owner-only path", request: "protocol=https\nhost=github.com\npath=work-org\n\n", want: "bob"},
		{name: "owner-only path with .git", request: "protocol=https\nhost=github.com\npath=work-org.git\n\n", want: "bob"},
		{name: "owner is case insensitive", request: "protocol=https\nhost=GitHub.com\npath=WORK-ORG/x.git\n\n", want: "bob"},
		{name: "owner beats cwd dir pin", inPin: true, request: "protocol=https\nhost=github.com\npath=work-org/x.git\n\n", want: "bob"},
		{name: "other owner falls to default", request: "protocol=https\nhost=github.com\npath=other-org/x.git\n\n", want: "alice"},
		{name: "other owner falls to cwd dir pin", inPin: true, request: "protocol=https\nhost=github.com\npath=other-org/x.git\n\n", want: "carol"},
		{name: "prefix of owner is not the owner", request: "protocol=https\nhost=github.com\npath=work-organization/x.git\n\n", want: "alice"},
		{name: "enterprise owner pin on its host", request: "protocol=https\nhost=ghe.example.test\npath=ghe-org/x.git\n\n", want: "dana"},
		{name: "owner pin does not cross hosts", request: "protocol=https\nhost=ghe.example.test\npath=work-org/x.git\n\n", wantErr: true},
		{name: "github owner pin is not used on enterprise", request: "protocol=https\nhost=github.com\npath=ghe-org/x.git\n\n", want: "alice"},
		{name: "missing host fails closed", request: "protocol=https\npath=work-org/x.git\n\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unpinned, pinned := credentialFixture(t, pins)
			if tc.inPin {
				t.Chdir(pinned)
			} else {
				t.Chdir(unpinned)
			}
			out, err := runCredentialGet(t, tc.request)
			if tc.wantErr {
				if err == nil || out != "quit=true\n\n" {
					t.Fatalf("want fail-closed, got %q (%v)", out, err)
				}
				return
			}
			if want := "username=x-access-token\npassword=tok-" + tc.want + "\n\n"; err != nil || out != want {
				t.Fatalf("got %q (%v), want %q", out, err, want)
			}
		})
	}
}

// The session override behaves like exec --user for github.com and leaves
// other hosts to their pins, since it cannot name a host.
func TestCredentialHelperSessionOverride(t *testing.T) {
	const pins = `pins:
  - user: bob
    owner: work-org
  - user: dana
    owner: ghe-org
    host: ghe.example.test
  - user: carol
    dir: "{{work}}"
`
	for _, tc := range []struct {
		name, request, want string
		inPin, wantErr      bool
	}{
		{name: "overrides the directory pin", inPin: true, request: "protocol=https\nhost=github.com\n\n", want: "erin"},
		{name: "overrides an owner pin", request: "protocol=https\nhost=github.com\npath=work-org/x.git\n\n", want: "erin"},
		{name: "overrides the default", request: "protocol=https\nhost=github.com\n\n", want: "erin"},
		{name: "enterprise host keeps its pin", request: "protocol=https\nhost=ghe.example.test\npath=ghe-org/x.git\n\n", want: "dana"},
		{name: "missing host still fails closed", request: "protocol=https\npath=work-org/x.git\n\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unpinned, pinned := credentialFixture(t, pins)
			t.Setenv("GH_AUTOACCOUNT_USER", "erin")
			if tc.inPin {
				t.Chdir(pinned)
			} else {
				t.Chdir(unpinned)
			}
			out, err := runCredentialGet(t, tc.request)
			if tc.wantErr {
				if err == nil || out != "quit=true\n\n" {
					t.Fatalf("want fail-closed, got %q (%v)", out, err)
				}
				return
			}
			if want := "username=x-access-token\npassword=tok-" + tc.want + "\n\n"; err != nil || out != want {
				t.Fatalf("got %q (%v), want %q", out, err, want)
			}
		})
	}
	t.Run("invalid override leaves other hosts on their pins", func(t *testing.T) {
		unpinned, _ := credentialFixture(t, pins)
		t.Setenv("GH_AUTOACCOUNT_USER", "-bad")
		t.Chdir(unpinned)
		out, err := runCredentialGet(t, "protocol=https\nhost=ghe.example.test\npath=ghe-org/x.git\n\n")
		if want := "username=x-access-token\npassword=tok-dana\n\n"; err != nil || out != want {
			t.Fatalf("got %q (%v), want %q", out, err, want)
		}
	})
	t.Run("invalid override fails closed", func(t *testing.T) {
		unpinned, _ := credentialFixture(t, pins)
		t.Setenv("GH_AUTOACCOUNT_USER", "-bad")
		t.Chdir(unpinned)
		if out, err := runCredentialGet(t, "protocol=https\nhost=github.com\n\n"); err == nil || out != "quit=true\n\n" {
			t.Fatalf("want fail-closed, got %q (%v)", out, err)
		}
	})
}

func TestCredentialHelperIgnoresNonHTTPSAndFailsClosed(t *testing.T) {
	unpinned, _ := credentialFixture(t, "pins:\n  - user: bob\n    owner: work-org\n")
	t.Chdir(unpinned)
	if out, err := runCredentialGet(t, "protocol=ssh\nhost=github.com\npath=work-org/x.git\n\n"); err != nil || out != "" {
		t.Fatalf("non-https request answered: %q %v", out, err)
	}
	// A broken credential for the owner pin must not fall back to the default account.
	gh := filepath.Join(filepath.Dir(unpinned), "gh")
	if err := os.WriteFile(gh, []byte("#!/bin/sh\n[ \"$4\" = alice ] && echo tok-alice || exit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, err := runCredentialGet(t, "protocol=https\nhost=github.com\npath=work-org/x.git\n\n")
	if err == nil || out != "quit=true\n\n" || strings.Contains(out, "alice") {
		t.Fatalf("broken owner credential did not fail closed: %q %v", out, err)
	}
}

func TestCredentialHelperRejectsInvalidRegistry(t *testing.T) {
	unpinned, _ := credentialFixture(t, "pins:\n  - user: bob\n    owner: work-org\n  - user: carol\n    owner: WORK-ORG\n")
	t.Chdir(unpinned)
	out, err := runCredentialGet(t, "protocol=https\nhost=github.com\npath=work-org/x.git\n\n")
	if err == nil || out != "quit=true\n\n" {
		t.Fatalf("duplicate owner pins accepted: %q %v", out, err)
	}
}

// A checkout's submodule under an owner-pinned organization authenticates as
// the owner pin, but only on the pin's exact host.
func TestCredentialHelperSubmoduleOwnerSelectsPinOnlyOnItsHost(t *testing.T) {
	_, pinned := credentialFixture(t, "pins:\n  - user: alice\n    dir: \"{{work}}\"\n  - user: bob\n    owner: work-org\n")
	t.Chdir(pinned)
	out, err := runCredentialGet(t, "protocol=https\nhost=github.com\npath=work-org/sub.git\n\n")
	if want := "username=x-access-token\npassword=tok-bob\n\n"; err != nil || out != want {
		t.Fatalf("got %q (%v), want %q", out, err, want)
	}
	out, err = runCredentialGet(t, "protocol=https\nhost=evil.example\npath=work-org/sub.git\n\n")
	if err == nil || out != "quit=true\n\n" || strings.Contains(out, "bob") {
		t.Fatalf("foreign host must fail closed without bob's credential: %q (%v)", out, err)
	}
}
