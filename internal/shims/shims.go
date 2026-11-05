package shims

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

const (
	shimMarkerStart = "# gh-autoprofile-shims:start"
	shimMarkerEnd   = "# gh-autoprofile-shims:end"
	shimSignature   = "# gh-autoprofile-gh-shim"
)

// Dir returns the gh-autoaccount shim directory path. On Windows it is a
// directory of its own, because setup puts all of it ahead of other gh installs
// on the user PATH.
func Dir() (string, error) {
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		if local == "" || !filepath.IsAbs(local) {
			return "", fmt.Errorf("LOCALAPPDATA must be set to an absolute path")
		}
		return filepath.Join(local, "gh-autoaccount", "bin"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// GHPath returns the path to the installed gh shim.
func GHPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, config.ExeName("gh")), nil
}

// CheckGHReplaceable rejects unmanaged paths before setup starts a transition.
func CheckGHReplaceable() error {
	path, err := GHPath()
	if err != nil {
		return err
	}
	return ensureManagedOrMissing(path)
}

// IsGHInstalled reports whether the gh shim exists.
func IsGHInstalled() bool {
	shimPath, err := GHPath()
	if err != nil {
		return false
	}
	return IsNativeGHInstalled() || (ensureManagedOrMissing(shimPath) == nil && fileExists(shimPath) && !isSymlink(shimPath))
}

// InjectPath prepends the shim dir to the shell RC file.
func InjectPath(rcPath, shimDir string) error {
	block := shimMarkerStart + "\n" +
		fmt.Sprintf(`export PATH=%s:"$PATH"`, shellQuote(shimDir)) + "\n" +
		shimMarkerEnd + "\n"

	existing, err := os.ReadFile(rcPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot read %s: %w", rcPath, err)
	}
	content := string(existing)

	if strings.Contains(content, shimMarkerStart) {
		startIdx := strings.Index(content, shimMarkerStart)
		endIdx := strings.Index(content, shimMarkerEnd)
		if endIdx != -1 {
			endIdx += len(shimMarkerEnd)
			if endIdx < len(content) && content[endIdx] == '\n' {
				endIdx++
			}
			content = content[:startIdx] + block + content[endIdx:]
			return config.WriteAtomicPreserveMode(rcPath, []byte(content), 0644)
		}
	}

	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + block
	return config.WriteAtomicPreserveMode(rcPath, []byte(content), 0644)
}

func ensureManagedOrMissing(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect existing gh path: %w", err)
	}
	if copyProxy && managedNativeLink(path) {
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if managedNativeLink(path) {
			return nil
		}
		return fmt.Errorf("refusing to replace symlink at %s", path)
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to replace directory at %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot inspect existing gh executable: %w", err)
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return fmt.Errorf("cannot inspect existing gh executable: %w", err)
	}
	content := string(buf[:n])
	if !isManagedShimContent(content) {
		return fmt.Errorf("refusing to overwrite unmanaged gh executable at %s; move it first or choose external shell integration", path)
	}
	return nil
}

func isManagedShimContent(content string) bool {
	if strings.HasPrefix(content, "#!/usr/bin/env bash\n"+shimSignature+"\nset -euo pipefail\n") {
		return true
	}
	return strings.HasPrefix(content, "#!/usr/bin/env bash\nset -euo pipefail\n") &&
		strings.Contains(content, "GH_AUTOPROFILE_DISABLE_SHIM") &&
		strings.Contains(content, "GH_AUTOPROFILE_REAL_GH=") &&
		strings.Contains(content, ` __shim-gh "$@"`)
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// CheckPathConfigured reports whether the shim directory is on the current PATH
// or persistently configured: the managed PATH block in common shell RC files,
// or the user PATH on Windows.
func CheckPathConfigured() bool {
	if shimDir, err := Dir(); err == nil {
		for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
			if entry != "" && config.SamePath(filepath.Clean(entry), shimDir) {
				return true
			}
		}
		if runtime.GOOS == "windows" {
			return userPathHas(shimDir)
		}
	}
	return CheckPathBlockInstalled()
}

// CheckPathBlockInstalled reports only the managed PATH block in shell RC files.
// Unlike CheckPathConfigured it ignores the process PATH, which may contain the
// shim directory without any gh-autoaccount installation.
func CheckPathBlockInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, name := range []string{".zshrc", ".zprofile", ".bashrc", ".bash_profile", ".bash_login", ".profile"} {
		if HasPathBlock(filepath.Join(home, name)) {
			return true
		}
	}
	return false
}

// HasPathBlock reports whether the file contains the managed PATH block.
func HasPathBlock(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), shimMarkerStart)
}
