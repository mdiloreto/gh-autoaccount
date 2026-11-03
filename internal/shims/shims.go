package shims

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

const (
	shimMarkerStart = "# gh-autoprofile-shims:start"
	shimMarkerEnd   = "# gh-autoprofile-shims:end"
	shimSignature   = "# gh-autoprofile-gh-shim"
)

// Dir returns the gh-autoaccount shim directory path.
func Dir() (string, error) {
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
	return filepath.Join(dir, "gh"), nil
}

// CheckGHReplaceable rejects unmanaged paths before setup starts a transition.
func CheckGHReplaceable() error {
	path, err := GHPath()
	if err != nil {
		return err
	}
	return ensureManagedOrMissing(path)
}

// InstallGH installs the gh shim that delegates to gh-autoaccount.
func InstallGH(extensionBinaryPath, realGHPath string) (string, error) {
	shimPath, err := GHPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(shimPath), 0755); err != nil {
		return "", fmt.Errorf("cannot create shim directory: %w", err)
	}
	if err := ensureManagedOrMissing(shimPath); err != nil {
		return "", err
	}
	wasNative := managedNativeLink(shimPath)

	realGHQuoted := shellQuote(realGHPath)
	extQuoted := shellQuote(extensionBinaryPath)
	content := strings.Join([]string{
		"#!/usr/bin/env bash",
		shimSignature,
		"set -euo pipefail",
		fmt.Sprintf(`if [[ -n "${GH_AUTOPROFILE_DISABLE_SHIM:-}" ]]; then exec %s "$@"; fi`, realGHQuoted),
		fmt.Sprintf(`GH_PATH=%s GH_AUTOPROFILE_REAL_GH=%s exec %s __shim-gh "$@"`, realGHQuoted, realGHQuoted, extQuoted),
		"",
	}, "\n")

	if err := config.WriteAtomicMode(shimPath, []byte(content), 0755); err != nil {
		return "", fmt.Errorf("cannot write gh shim: %w", err)
	}
	if wasNative {
		if record, err := nativeRecordPath(); err == nil {
			if err := os.Remove(record); err != nil && !os.IsNotExist(err) {
				return "", fmt.Errorf("legacy shim installed but cannot remove native ownership record: %w", err)
			}
		}
	}
	return shimPath, nil
}

// IsGHInstalled reports whether the gh shim exists.
func IsGHInstalled() bool {
	shimPath, err := GHPath()
	if err != nil {
		return false
	}
	return IsNativeGHInstalled() || (ensureManagedOrMissing(shimPath) == nil && fileExists(shimPath) && !isSymlink(shimPath))
}

// TargetsGH reports whether the installed managed shim points to realGHPath.
// Legacy shims return false so setup --migrate refreshes their quoting too.
func TargetsGH(realGHPath string) (bool, error) {
	if IsNativeGHInstalled() {
		return true, nil
	}
	shimPath, err := GHPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(shimPath)
	if err != nil {
		return false, fmt.Errorf("cannot read gh shim: %w", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "#!/usr/bin/env bash\n"+shimSignature+"\nset -euo pipefail\n") {
		return false, nil
	}
	quoted := shellQuote(realGHPath)
	return strings.Contains(content, `then exec `+quoted+` "$@"; fi`) &&
		strings.Contains(content, `GH_PATH=`+quoted+` GH_AUTOPROFILE_REAL_GH=`+quoted+` exec `), nil
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

// CheckPathConfigured checks if the shim PATH block exists in common shell RC files.
func CheckPathConfigured() bool {
	if shimDir, err := Dir(); err == nil {
		for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
			if entry == shimDir {
				return true
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	files := []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), shimMarkerStart) {
			return true
		}
	}
	return false
}
