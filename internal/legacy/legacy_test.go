package legacy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	managedBlock = "# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\n"
	hookBlock    = "# gh-autoprofile-hook:start\nsource /home/u/.config/gh-autoprofile/hook.sh\n# gh-autoprofile-hook:end\n"
)

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// legacyHome points every location the package resolves at a temp tree.
func legacyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("DIRENV_CONFIG", filepath.Join(home, "direnv"))
	return home
}

// fakeDirenv puts a direnv on PATH that appends its arguments to a log, and
// returns the log path.
func fakeDirenv(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("TEST_DIRENV_LOG", log)
	write(t, filepath.Join(bin, "direnv"), script, 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

const recordingDirenv = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_DIRENV_LOG\"\n"

func TestFindBlock(t *testing.T) {
	const s, e = envrcStart, envrcEnd
	for _, tc := range []struct {
		name, content string
		start, end    int
		malformed     bool
	}{
		{name: "empty", content: "", start: -1, end: -1},
		{name: "no markers", content: "export A=1\n", start: -1, end: -1},
		{name: "one block", content: "a\n" + s + "\nx\n" + e + "\nb\n", start: 2, end: len("a\n" + s + "\nx\n" + e + "\n")},
		{name: "block without final newline", content: s + "\n" + e, start: 0, end: len(s + "\n" + e)},
		{name: "CRLF markers", content: s + "\r\nx\r\n" + e + "\r\n", start: 0, end: len(s + "\r\nx\r\n" + e + "\r\n")},
		{name: "start only", content: s + "\n", malformed: true},
		{name: "end only", content: e + "\n", malformed: true},
		{name: "end before start", content: e + "\n" + s + "\n", malformed: true},
		{name: "two blocks", content: s + "\n" + e + "\n" + s + "\n" + e + "\n", malformed: true},
		{name: "start marker inside a longer line", content: "echo '" + s + "'\n", malformed: true},
		{name: "indented marker", content: "  " + s + "\n" + e + "\n", malformed: true},
		{name: "end marker inside a longer line", content: s + "\nx " + e + "\n", malformed: true},
		{name: "block plus embedded marker", content: s + "\n" + e + "\n# " + s + " again\n", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start, end, err := findBlock(tc.content, s, e)
			if tc.malformed {
				if err == nil {
					t.Fatalf("accepted malformed content: %d %d", start, end)
				}
				return
			}
			if err != nil || start != tc.start || end != tc.end {
				t.Fatalf("got %d %d %v, want %d %d", start, end, err, tc.start, tc.end)
			}
		})
	}
}

func TestRemoveHookSourceKeepsOtherBytes(t *testing.T) {
	home := legacyHome(t)
	rc := filepath.Join(home, ".bashrc")
	const before = "# mine\r\nexport A=1\n"
	const after = "export PATH=\"$HOME/bin:$PATH\"\n# gh-autoprofile-shims:start\nexport PATH=x:\"$PATH\"\n# gh-autoprofile-shims:end"
	write(t, rc, before+hookBlock+after, 0640)
	if !HasHookSource(rc) {
		t.Fatal("hook source not detected")
	}
	removed, err := RemoveHookSource(rc)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if got := read(t, rc); got != before+after {
		t.Fatalf("bytes changed beyond the block: %q", got)
	}
	if info, err := os.Stat(rc); err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode not preserved: %v %v", info, err)
	}
	if HasHookSource(rc) {
		t.Fatal("hook source still detected")
	}
	if removed, err := RemoveHookSource(rc); err != nil || removed {
		t.Fatalf("second removal: removed=%v err=%v", removed, err)
	}
}

func TestRemoveHookSourceEditsSymlinkTarget(t *testing.T) {
	home := legacyHome(t)
	target := filepath.Join(home, "dotfiles", "bashrc")
	write(t, target, "before\n"+hookBlock+"after\n", 0600)
	rc := filepath.Join(home, ".bashrc")
	if err := os.Symlink(target, rc); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveHookSource(rc); err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if link, err := os.Readlink(rc); err != nil || link != target {
		t.Fatalf("symlink replaced: %q %v", link, err)
	}
	if got := read(t, target); got != "before\nafter\n" {
		t.Fatalf("target = %q", got)
	}
}

func TestRemoveHookSourceMalformedAndMissing(t *testing.T) {
	home := legacyHome(t)
	if removed, err := RemoveHookSource(filepath.Join(home, ".zshrc")); err != nil || removed {
		t.Fatalf("missing file: removed=%v err=%v", removed, err)
	}
	rc := filepath.Join(home, ".bashrc")
	const content = "before\n# gh-autoprofile-hook:start\nsource x\n"
	write(t, rc, content, 0600)
	removed, err := RemoveHookSource(rc)
	if err == nil || removed || !strings.Contains(err.Error(), rc) {
		t.Fatalf("malformed block: removed=%v err=%v", removed, err)
	}
	if got := read(t, rc); got != content {
		t.Fatalf("malformed file edited: %q", got)
	}
}

func TestRemoveLibraryAndHookRequireOurHeader(t *testing.T) {
	legacyHome(t)
	lib, err := ShellLibPath()
	if err != nil {
		t.Fatal(err)
	}
	hook, err := ShellHookPath()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		path   string
		header string
		remove func() (bool, error)
	}{
		{"library", lib, libHeader, RemoveLibrary},
		{"hook", hook, hookHeader, RemoveHook},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if removed, err := tc.remove(); err != nil || removed {
				t.Fatalf("absent file: removed=%v err=%v", removed, err)
			}

			write(t, tc.path, "#!/bin/sh\n# someone else's file\n", 0644)
			if removed, err := tc.remove(); err == nil || removed {
				t.Fatalf("removed a file without our header: removed=%v err=%v", removed, err)
			}
			if got := read(t, tc.path); got != "#!/bin/sh\n# someone else's file\n" {
				t.Fatalf("foreign file changed: %q", got)
			}

			if err := os.Remove(tc.path); err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(t.TempDir(), "real")
			write(t, real, tc.header+"\n", 0644)
			if err := os.Symlink(real, tc.path); err != nil {
				t.Fatal(err)
			}
			if removed, err := tc.remove(); err == nil || removed {
				t.Fatalf("followed a symlink: removed=%v err=%v", removed, err)
			}
			if _, err := os.Lstat(real); err != nil {
				t.Fatalf("symlink target removed: %v", err)
			}
			if _, err := os.Lstat(tc.path); err != nil {
				t.Fatalf("symlink removed: %v", err)
			}

			if err := os.Remove(tc.path); err != nil {
				t.Fatal(err)
			}
			write(t, tc.path, "#!/bin/sh\n"+tc.header+"\n", 0644)
			if got := LeftoverFiles(); len(got) != 1 || got[0] != tc.path {
				t.Fatalf("LeftoverFiles = %v, want [%s]", got, tc.path)
			}
			if removed, err := tc.remove(); err != nil || !removed {
				t.Fatalf("owned file: removed=%v err=%v", removed, err)
			}
			if _, err := os.Lstat(tc.path); !os.IsNotExist(err) {
				t.Fatalf("owned file still present: %v", err)
			}
		})
	}
}

func TestCleanupManagedEnvrcKeepsCustomBytes(t *testing.T) {
	legacyHome(t)
	log := fakeDirenv(t, recordingDirenv)
	dir := t.TempDir()
	path := filepath.Join(dir, ".envrc")
	const before, after = "export CUSTOM=1\n", "export OTHER=2\n"
	write(t, path, before+managedBlock+after, 0640)
	if !HasManagedEnvrc(dir) {
		t.Fatal("managed block not detected")
	}
	cleaned, err := CleanupManagedEnvrc(dir)
	if err != nil || !cleaned {
		t.Fatalf("cleaned=%v err=%v", cleaned, err)
	}
	if got := read(t, path); got != before+after {
		t.Fatalf("custom bytes changed: %q", got)
	}
	if got := read(t, log); got != "deny "+path+"\n" {
		t.Fatalf("direnv calls = %q", got)
	}
	if HasManagedEnvrc(dir) {
		t.Fatal("block still detected")
	}
	// A second pass has nothing to do and must not revoke anything again.
	if cleaned, err := CleanupManagedEnvrc(dir); err != nil || cleaned {
		t.Fatalf("second cleanup: cleaned=%v err=%v", cleaned, err)
	}
	if got := read(t, log); got != "deny "+path+"\n" {
		t.Fatalf("second pass called direnv: %q", got)
	}
}

func TestCleanupManagedEnvrcDeletesFileWhenOnlyBlock(t *testing.T) {
	legacyHome(t)
	log := fakeDirenv(t, recordingDirenv)
	dir := t.TempDir()
	path := filepath.Join(dir, ".envrc")
	write(t, path, managedBlock, 0600)
	if cleaned, err := CleanupManagedEnvrc(dir); err != nil || !cleaned {
		t.Fatalf("cleaned=%v err=%v", cleaned, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf(".envrc not removed: %v", err)
	}
	if got := read(t, log); got != "deny "+path+"\n" {
		t.Fatalf("direnv calls = %q", got)
	}
}

func TestCleanupManagedEnvrcNoFileOrNoBlock(t *testing.T) {
	legacyHome(t)
	log := fakeDirenv(t, recordingDirenv)
	dir := t.TempDir()
	if cleaned, err := CleanupManagedEnvrc(dir); err != nil || cleaned {
		t.Fatalf("missing file: cleaned=%v err=%v", cleaned, err)
	}
	write(t, filepath.Join(dir, ".envrc"), "export A=1\n", 0600)
	if cleaned, err := CleanupManagedEnvrc(dir); err != nil || cleaned {
		t.Fatalf("no block: cleaned=%v err=%v", cleaned, err)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("direnv called without a block: %v", err)
	}
}

func TestCleanupManagedEnvrcRefusals(t *testing.T) {
	for _, kind := range []string{"symlink", "malformed", "deny fails", "no direnv", "changed during deny"} {
		t.Run(kind, func(t *testing.T) {
			legacyHome(t)
			script := recordingDirenv
			switch kind {
			case "deny fails":
				script = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_DIRENV_LOG\"\nexit 1\n"
			case "changed during deny":
				script = "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$TEST_DIRENV_LOG\"\nprintf 'late edit\\n' >> \"$2\"\n"
			}
			log := fakeDirenv(t, script)
			if kind == "no direnv" {
				t.Setenv("PATH", t.TempDir())
			}
			dir := t.TempDir()
			path := filepath.Join(dir, ".envrc")
			content := "keep\n" + managedBlock
			switch kind {
			case "symlink":
				target := filepath.Join(t.TempDir(), "target")
				write(t, target, content, 0600)
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				path = target
			case "malformed":
				content = "keep\n# gh-autoprofile:start\n"
				write(t, path, content, 0600)
			default:
				write(t, path, content, 0600)
			}
			cleaned, err := CleanupManagedEnvrc(dir)
			if err == nil || cleaned {
				t.Fatalf("cleaned=%v err=%v", cleaned, err)
			}
			want := content
			if kind == "changed during deny" {
				want += "late edit\n"
			}
			if got := read(t, path); got != want {
				t.Fatalf("file = %q, want %q", got, want)
			}
			if kind == "symlink" || kind == "malformed" || kind == "no direnv" {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("direnv called: %v", err)
				}
			}
		})
	}
}
