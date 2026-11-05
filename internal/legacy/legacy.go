// Package legacy finds and removes what the retired direnv wrapper and export
// modes left on disk. It never installs anything, and it only touches content
// that carries this project's markers or file headers.
package legacy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

const (
	envrcStart = "# gh-autoprofile:start"
	envrcEnd   = "# gh-autoprofile:end"
	hookStart  = "# gh-autoprofile-hook:start"
	hookEnd    = "# gh-autoprofile-hook:end"

	libHeader  = "# gh-autoprofile direnv shell library"
	hookHeader = "# gh-autoprofile shell hook"
)

// ShellLibPath is where setup used to install the direnv library, under
// direnv's active config root.
func ShellLibPath() (string, error) {
	if root := os.Getenv("DIRENV_CONFIG"); root != "" {
		return filepath.Join(root, "lib", "gh-autoprofile.sh"), nil
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "direnv", "lib", "gh-autoprofile.sh"), nil
}

// ShellHookPath is where setup used to install the prompt hook.
func ShellHookPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hook.sh"), nil
}

// RCFiles are the shell startup files that may source the hook: setup wrote its
// block into some of them, users were told to add the line by hand when it could
// not, and modular configs source it from their own directories.
func RCFiles() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, name := range []string{".zshenv", ".zprofile", ".zshrc", ".bashrc", ".bash_profile", ".bash_login", ".profile"} {
		files = append(files, filepath.Join(home, name))
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	dirs := []string{filepath.Join(configHome, "shell"), filepath.Join(configHome, "zsh"), filepath.Join(configHome, "bash")}
	if zdot := os.Getenv("ZDOTDIR"); zdot != "" {
		dirs = append(dirs, zdot)
	}
	for _, dir := range dirs {
		files = append(files, shellConfigFiles(dir)...)
	}
	return files, nil
}

// shellConfigFiles lists the small regular files in a modular shell config
// directory. The walk is bounded so a large tree cannot stall setup.
func shellConfigFiles(root string) []string {
	const maxDepth, maxSize = 4, 1 << 20
	var files []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if rel, err := filepath.Rel(root, path); err == nil && strings.Count(rel, string(filepath.Separator)) >= maxDepth {
				return filepath.SkipDir
			}
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && info.Size() <= maxSize {
			files = append(files, path)
		}
		return nil
	})
	return files
}

// HasHookSource reports whether an RC file still has the managed hook block.
func HasHookSource(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte(hookStart))
}

// ReferencesHook reports whether a startup file mentions the hook file at all,
// inside the managed block or in a line the user added by hand.
func ReferencesHook(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && bytes.Contains(data, []byte("gh-autoprofile/hook.sh"))
}

// RemoveHookSource deletes the managed hook-source block from an RC file and
// keeps every other byte. A symlinked RC file is edited at its target.
func RemoveHookSource(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	start, end, err := findBlock(string(data), hookStart, hookEnd)
	if err != nil || start == -1 {
		if err != nil {
			err = fmt.Errorf("%w: %s", err, path)
		}
		return false, err
	}
	remaining := append(append([]byte(nil), data[:start]...), data[end:]...)
	return true, config.WriteAtomicPreserveMode(path, remaining, 0644)
}

// LeftoverFiles lists the installed library and hook files that still exist.
func LeftoverFiles() []string {
	var found []string
	for _, locate := range []func() (string, error){ShellLibPath, ShellHookPath} {
		if path, err := locate(); err == nil {
			if _, err := os.Lstat(path); err == nil {
				found = append(found, path)
			}
		}
	}
	return found
}

// RemoveLibrary deletes the installed direnv library if it is ours.
func RemoveLibrary() (bool, error) { return removeOwned(ShellLibPath, libHeader) }

// RemoveHook deletes the installed prompt hook if it is ours.
func RemoveHook() (bool, error) { return removeOwned(ShellHookPath, hookHeader) }

func removeOwned(locate func() (string, error), header string) (bool, error) {
	path, err := locate()
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || !hasHeader(data, header) {
		return false, fmt.Errorf("not removing %s: it is not a file this tool installed", path)
	}
	return true, os.Remove(path)
}

// hasHeader matches the generated layout, a shebang followed by the header
// line, so a user file that merely quotes the header is not taken for ours.
func hasHeader(data []byte, header string) bool {
	lines := bytes.SplitN(data, []byte("\n"), 3)
	return len(lines) >= 2 && bytes.HasPrefix(lines[0], []byte("#!")) && bytes.HasPrefix(lines[1], []byte(header))
}

// HasManagedEnvrc reports whether dir's .envrc still has a managed block or a
// stray marker.
func HasManagedEnvrc(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, ".envrc"))
	return err == nil && (bytes.Contains(data, []byte(envrcStart)) || bytes.Contains(data, []byte(envrcEnd)))
}

// CleanupManagedEnvrc revokes direnv authorization, then removes only the
// managed block, deleting the file when nothing else remains. Symlinks,
// malformed markers and a file that changes during cleanup are refused.
func CleanupManagedEnvrc(dir string) (bool, error) {
	path := filepath.Join(dir, ".envrc")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refusing non-regular .envrc: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	start, end, err := findBlock(string(data), envrcStart, envrcEnd)
	if err != nil {
		return false, fmt.Errorf("%w: %s", err, path)
	}
	if start == -1 {
		return false, nil
	}
	if _, err := exec.LookPath("direnv"); err != nil {
		return false, fmt.Errorf("direnv required to revoke authorization for %s", path)
	}
	cmd := exec.Command("direnv", "deny", path)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("direnv deny failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || !currentInfo.Mode().IsRegular() || !os.SameFile(info, currentInfo) {
		return false, fmt.Errorf("authorization revoked but .envrc changed during cleanup; review it and run direnv allow manually if appropriate")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, current) {
		return false, fmt.Errorf("authorization revoked but .envrc changed during cleanup; review it and run direnv allow manually if appropriate")
	}
	remaining := append(append([]byte(nil), data[:start]...), data[end:]...)
	if len(remaining) == 0 {
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("authorization revoked but .envrc removal failed: %w; review .envrc and run direnv allow manually if appropriate", err)
		}
	} else if err := config.WriteAtomicMode(path, remaining, 0600); err != nil {
		return false, fmt.Errorf("authorization revoked but .envrc rewrite failed: %w; review .envrc and run direnv allow manually if appropriate", err)
	}
	return true, nil
}

// findBlock returns the byte range of the single block delimited by whole
// marker lines, or -1 when neither marker appears. Any other arrangement,
// including a marker embedded in a longer line, is malformed.
func findBlock(content, startMarker, endMarker string) (int, int, error) {
	start, end := -1, -1
	for pos := 0; pos < len(content); {
		lineEnd := len(content)
		if next := strings.IndexByte(content[pos:], '\n'); next >= 0 {
			lineEnd = pos + next + 1
		}
		line := strings.TrimSuffix(strings.TrimSuffix(content[pos:lineEnd], "\n"), "\r")
		if strings.Contains(line, startMarker) || strings.Contains(line, endMarker) {
			switch {
			case line == startMarker && start == -1 && end == -1:
				start = pos
			case line == endMarker && start != -1 && end == -1:
				end = lineEnd
			default:
				return 0, 0, fmt.Errorf("malformed managed markers")
			}
		}
		pos = lineEnd
	}
	if start == -1 && end == -1 {
		return -1, -1, nil
	}
	if start == -1 || end == -1 {
		return 0, 0, fmt.Errorf("malformed managed markers")
	}
	return start, end, nil
}
