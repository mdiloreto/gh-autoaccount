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

func TestShellLibPathConfigRoots(t *testing.T) {
	home, absolute := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Skipf("os.UserHomeDir does not use HOME or USERPROFILE: %q, %v", got, err)
	}
	for _, tc := range []struct {
		name, xdg, direnv, want string
	}{
		{name: "default", want: filepath.Join(home, ".config", "direnv", "lib", "gh-autoprofile.sh")},
		{name: "relative XDG", xdg: ".", want: filepath.Join(home, ".config", "direnv", "lib", "gh-autoprofile.sh")},
		{name: "absolute XDG", xdg: absolute, want: filepath.Join(absolute, "direnv", "lib", "gh-autoprofile.sh")},
		{name: "relative direnv override", xdg: absolute, direnv: "direnv-override", want: filepath.Join("direnv-override", "lib", "gh-autoprofile.sh")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tc.xdg)
			t.Setenv("DIRENV_CONFIG", tc.direnv)
			if got, err := ShellLibPath(); err != nil || got != tc.want {
				t.Fatalf("ShellLibPath() = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestHookReferencesIgnoresRelativeXDGConfigHome(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got, err := os.UserHomeDir(); err != nil || got != home {
		t.Skipf("os.UserHomeDir does not use HOME or USERPROFILE: %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", ".")
	t.Setenv("ZDOTDIR", "")
	t.Chdir(cwd)
	trusted := filepath.Join(home, ".config", "shell", "hook.zsh")
	foreign := filepath.Join(cwd, "shell", "hook.zsh")
	for _, path := range []string{trusted, foreign} {
		write(t, path, "source ~/.config/gh-autoprofile/hook.sh\n", 0600)
	}
	refs, complete := HookReferences()
	if !complete || len(refs) != 1 || refs[0] != trusted {
		t.Fatalf("HookReferences() = %v, %v; want only %q", refs, complete, trusted)
	}
}

func TestLegacyRejectsRelativeHome(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("HOME", ".")
	t.Setenv("USERPROFILE", ".")
	if got, err := os.UserHomeDir(); err != nil || got != "." {
		t.Skipf("os.UserHomeDir does not use HOME or USERPROFILE: %q, %v", got, err)
	}
	t.Setenv("XDG_CONFIG_HOME", ".")
	t.Setenv("DIRENV_CONFIG", "")
	t.Setenv("ZDOTDIR", "")
	t.Chdir(cwd)
	write(t, filepath.Join(cwd, ".bashrc"), "source ~/.config/gh-autoprofile/hook.sh\n", 0600)
	if path, err := ShellLibPath(); err == nil || path != "" {
		t.Fatalf("ShellLibPath() = %q, %v; want a home-directory error", path, err)
	}
	if files, err := RCFiles(); err == nil || files != nil {
		t.Fatalf("RCFiles() = %v, %v; want a home-directory error", files, err)
	}
	if refs, complete := HookReferences(); complete || refs != nil {
		t.Fatalf("HookReferences() = %v, %v; want an incomplete scan", refs, complete)
	}
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

			// A user file that only quotes our header further down is not ours.
			for _, foreign := range []string{"#!/bin/sh\n# someone else's file\n", "#!/bin/sh\n# my own copy\n# based on " + tc.header + "\n"} {
				write(t, tc.path, foreign, 0644)
				if removed, err := tc.remove(); err == nil || removed {
					t.Fatalf("removed a file without our header: removed=%v err=%v", removed, err)
				}
				if got := read(t, tc.path); got != foreign {
					t.Fatalf("foreign file changed: %q", got)
				}
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

func TestHookReferences(t *testing.T) {
	const line = "source \"$HOME/.config/gh-autoprofile/hook.sh\"\n"
	setup := func(t *testing.T) (home, config string) {
		home = legacyHome(t)
		t.Setenv("ZDOTDIR", "")
		return home, filepath.Join(home, "config")
	}
	t.Run("modular configs, symlinks and ZDOTDIR", func(t *testing.T) {
		home, config := setup(t)
		dotfiles := filepath.Join(home, "dotfiles")
		zdot := filepath.Join(home, "zdot")
		t.Setenv("ZDOTDIR", zdot)
		want := []string{
			filepath.Join(config, "shell", "tools", "gh-autoprofile.zsh"),
			filepath.Join(config, "bash", "init.bash"),
			filepath.Join(zdot, ".zshrc"),
		}
		for _, path := range want {
			write(t, path, line, 0644)
		}
		// chezmoi-style links: a linked file, and a linked directory.
		write(t, filepath.Join(dotfiles, "linked.zsh"), line, 0644)
		write(t, filepath.Join(dotfiles, "zsh", "conf.d", "x.zsh"), line, 0644)
		if err := os.Symlink(filepath.Join(dotfiles, "linked.zsh"), filepath.Join(config, "shell", "linked.zsh")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(dotfiles, "zsh"), filepath.Join(config, "zsh")); err != nil {
			t.Fatal(err)
		}
		want = append(want, filepath.Join(config, "shell", "linked.zsh"), filepath.Join(config, "zsh", "conf.d", "x.zsh"))
		// Not sourced by zsh, so not scanned even though it mentions the hook.
		write(t, filepath.Join(zdot, "notes.txt"), line, 0644)
		// A managed block is removed by setup itself, so it is not a reference.
		write(t, filepath.Join(home, ".bashrc"), "# gh-autoprofile-hook:start\n"+line+"# gh-autoprofile-hook:end\n", 0644)
		// A loop back up the tree must terminate.
		if err := os.Symlink(filepath.Join(config, "shell"), filepath.Join(config, "shell", "loop")); err != nil {
			t.Fatal(err)
		}

		refs, complete := HookReferences()
		if !complete {
			t.Fatalf("scan reported incomplete: %v", refs)
		}
		got := map[string]bool{}
		for _, r := range refs {
			got[r] = true
		}
		for _, path := range want {
			if !got[path] {
				t.Errorf("reference not found: %s (got %v)", path, refs)
			}
		}
		for _, path := range []string{filepath.Join(zdot, "notes.txt"), filepath.Join(home, ".bashrc")} {
			if got[path] {
				t.Errorf("not a hand-written reference: %s", path)
			}
		}
	})
	t.Run("limits and errors make the scan incomplete", func(t *testing.T) {
		for name, prepare := range map[string]func(t *testing.T, config string){
			"too deep": func(t *testing.T, config string) {
				write(t, filepath.Join(config, "shell", "a", "b", "c", "d", "e", "f", "g", "deep.zsh"), "echo\n", 0644)
			},
			"file too large": func(t *testing.T, config string) {
				write(t, filepath.Join(config, "shell", "big.zsh"), strings.Repeat("x", scanMaxFile+1), 0644)
			},
			"unreadable directory": func(t *testing.T, config string) {
				dir := filepath.Join(config, "shell", "private")
				write(t, filepath.Join(dir, "x.zsh"), "echo\n", 0644)
				if err := os.Chmod(dir, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.Chmod(dir, 0700) })
			},
		} {
			t.Run(name, func(t *testing.T) {
				if name == "unreadable directory" && os.Geteuid() == 0 {
					t.Skip("root can read any directory")
				}
				_, config := setup(t)
				prepare(t, config)
				if _, complete := HookReferences(); complete {
					t.Fatal("scan must report incomplete")
				}
			})
		}
	})
}
