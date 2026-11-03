package scripts

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPrereleaseLauncher(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("fixture supports linux-amd64 and linux-arm64")
	}
	root := t.TempDir()
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"GH_CONFIG_DIR=" + filepath.Join(root, "gh"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(root, "no-bus"),
		"LC_ALL=C", "TZ=UTC",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	launcher, err := os.ReadFile("../gh-autoaccount")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gh-autoaccount"), launcher, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/usr/bin/git", args...)
		cmd.Dir = root
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	git("init", "-q")
	git("add", "gh-autoaccount")
	git("-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "test")
	asset := "gh-autoaccount_v1.0.0-beta.1_linux-" + runtime.GOARCH
	content := []byte("#!/bin/sh\nprintf 'fixture:%s\\n' \"$*\"\n")
	sum := sha256.Sum256(content)
	fixtures := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixtures, asset), content, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixtures, "checksums.txt"), []byte(fmt.Sprintf("%x  %s\n", sum, asset)), 0600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(fake, []byte(`#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$CALLS"
if [ "$#" -eq 6 ] && [ "$1" = api ] && [ "$2" = --hostname ] && [ "$3" = github.com ] && [ "$4" = repos/mdiloreto/gh-autoaccount/commits/v1.0.0-beta.1 ] && [ "$5" = --jq ] && [ "$6" = .sha ]; then
  if [ "$MISMATCH" = 1 ]; then echo mismatch; else /usr/bin/git -C "$REPO" rev-parse HEAD; fi
elif [ "$#" -eq 11 ] && [ "$1" = release ] && [ "$2" = download ] && [ "$3" = v1.0.0-beta.1 ] && [ "$4" = --repo ] && [ "$5" = github.com/mdiloreto/gh-autoaccount ] && [ "$6" = --pattern ] && [ "$7" = "$ASSET" ] && [ "$8" = --pattern ] && [ "$9" = checksums.txt ] && [ "${10}" = --dir ]; then
  /bin/cp "$FIXTURES/$ASSET" "$FIXTURES/checksums.txt" "${11}/"
else
  echo 'unexpected provider invocation' >&2
  exit 1
fi
`), 0755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(root, "calls")
	mismatch := "0"
	run := func(tag string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(root, "gh-autoaccount"), "test-argument")
		cmd.Env = append(env, "GH_PATH="+fake, "FIXTURES="+fixtures, "REPO="+root, "CALLS="+calls, "ASSET="+asset, "MISMATCH="+mismatch, "GH_AUTOPROFILE_RELEASE_TAG="+tag)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(""); err == nil || !strings.Contains(out, "no unique prerelease tag") {
		t.Fatalf("no tags: %v %s", err, out)
	}
	git("tag", "v1.0.0-beta.1")
	mismatch = "1"
	if out, err := run(""); err == nil || !strings.Contains(out, "does not match") {
		t.Fatalf("remote mismatch: %v %s", err, out)
	}
	mismatch = "0"
	if err := os.WriteFile(filepath.Join(fixtures, "checksums.txt"), []byte("bad checksum\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(""); err == nil || !strings.Contains(out, "release checksum verification failed") {
		t.Fatalf("bad release checksum: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(fixtures, "checksums.txt"), []byte(fmt.Sprintf("%x  %s\n", sum, asset)), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(""); err != nil || !strings.Contains(out, "fixture:test-argument") {
		t.Fatalf("unique tag: %v %s", err, out)
	}
	before, err := os.ReadFile(calls)
	if err != nil || strings.Count(string(before), "\n") != 5 {
		t.Fatalf("expected mismatch API, failed and successful downloads: %v %q", err, before)
	}
	if out, err := run(""); err != nil || !strings.Contains(out, "fixture:test-argument") {
		t.Fatalf("cached: %v %s", err, out)
	}
	after, err := os.ReadFile(calls)
	if err != nil || string(after) != string(before) {
		t.Fatalf("cache invoked provider: %v %q", err, after)
	}
	git("tag", "v1.0.0-beta.2")
	if out, err := run(""); err == nil || !strings.Contains(out, "no unique prerelease tag") {
		t.Fatalf("ambiguous: %v %s", err, out)
	}
	if out, err := run("v1.0.0-beta.1"); err != nil || !strings.Contains(out, "fixture:test-argument") {
		t.Fatalf("explicit: %v %s", err, out)
	}
	if out, err := run("v1.0.0-beta.3"); err == nil || !strings.Contains(out, "must name") {
		t.Fatalf("wrong tag: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, ".autoaccount-cache", "v1.0.0-beta.1-linux-"+runtime.GOARCH, asset), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := run("v1.0.0-beta.1"); err == nil || !strings.Contains(out, "failed checksum") {
		t.Fatalf("tampered: %v %s", err, out)
	}
}

func TestLauncherPreservesTrustedGHPath(t *testing.T) {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("fixture supports linux-amd64 and linux-arm64")
	}
	root := t.TempDir()
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"GH_CONFIG_DIR=" + filepath.Join(root, "gh"),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(root, "no-bus"),
		"LC_ALL=C", "TZ=UTC",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	}
	launcher, err := os.ReadFile("../gh-autoaccount")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gh-autoaccount"), launcher, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("/usr/bin/git", args...)
		command.Dir = root
		command.Env = env
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", "gh-autoaccount")
	git("-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qm", "test")
	git("tag", "v1.0.0-beta.1")
	commit := git("rev-parse", "HEAD")
	asset := "gh-autoaccount_v1.0.0-beta.1_linux-" + runtime.GOARCH
	entry := filepath.Join(root, ".autoaccount-cache", "v1.0.0-beta.1-linux-"+runtime.GOARCH)
	if err := os.MkdirAll(entry, 0700); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(entry, asset)
	build := exec.Command("go", "build", "-o", candidate, "./cmd/gh-autoaccount")
	build.Dir = ".."
	build.Env = append(append([]string{}, env...), "GOENV=off", "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	for _, key := range []string{"GOMODCACHE", "GOCACHE", "GOPATH", "GOROOT"} {
		if value, ok := os.LookupEnv(key); ok {
			build.Env = append(build.Env, key+"="+value)
		}
	}
	if _, ok := os.LookupEnv("GOMODCACHE"); !ok {
		if _, ok := os.LookupEnv("GOPATH"); !ok {
			home, err := os.UserHomeDir()
			if err != nil {
				t.Fatal(err)
			}
			build.Env = append(build.Env, "GOMODCACHE="+filepath.Join(home, "go", "pkg", "mod"))
		}
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	content, err := os.ReadFile(candidate)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if err := os.WriteFile(filepath.Join(entry, "checksums.txt"), []byte(fmt.Sprintf("%x  %s\n", sum, asset)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entry, "commit"), []byte(commit+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	trusted := filepath.Join(t.TempDir(), "trusted-gh")
	attackerDir := t.TempDir()
	attacker := filepath.Join(attackerDir, "gh")
	log := filepath.Join(root, "calls")
	trustedScript := "#!/bin/sh\nprintf 'trusted\\n' >> \"$TEST_LOG\"\nif [ \"$1\" = auth ]; then printf 'stored-token\\n'; else printf 'candidate-gh-path=%s\\n' \"$GH_PATH\"; fi\n"
	if err := os.WriteFile(trusted, []byte(trustedScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(attacker, []byte("#!/bin/sh\nprintf 'attacker\\n' >> \"$TEST_LOG\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(filepath.Join(root, "gh-autoaccount"), "exec", "--user", "alice", "--", "gh", "check")
	command.Dir = root
	command.Env = append(append([]string{}, env...), "GH_PATH="+trusted, "GH_AUTOPROFILE_REAL_GH="+attacker, "PATH="+attackerDir+":/usr/bin:/bin", "TEST_LOG="+log)
	out, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "candidate-gh-path="+trusted) {
		t.Fatalf("launcher candidate: %v %s", err, out)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "trusted\ntrusted\n" {
		t.Fatalf("calls %q: %v", calls, err)
	}
}
