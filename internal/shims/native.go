package shims

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func NativeGHSupported() bool { return runtime.GOOS == "linux" || runtime.GOOS == "darwin" }

func nativeRecordPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "native-gh-targets.json"), nil
}

func nativeTargets() []string {
	path, err := nativeRecordPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var targets []string
	if json.Unmarshal(data, &targets) != nil {
		return nil
	}
	return targets
}

func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func managedNativeLink(path string) bool {
	if !isSymlink(path) {
		return false
	}
	target, err := os.Readlink(path)
	if err != nil || !filepath.IsAbs(target) {
		return false
	}
	for _, recorded := range nativeTargets() {
		if target == recorded {
			return true
		}
	}
	return false
}

func IsNativeGHInstalled() bool {
	path, err := GHPath()
	return err == nil && workingNativeLink(path)
}

func workingNativeLink(path string) bool {
	if !managedNativeLink(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0
}

// InstallNativeGH installs an owned symlink without recording a real gh path.
func InstallNativeGH(extensionBinaryPath string) (string, error) {
	path, err := GHPath()
	if err != nil {
		return "", err
	}
	return installNativeLink(path, extensionBinaryPath)
}

// MiseGHDir is a directory that holds only the gh shim. mise can put it ahead of
// its tool directories without also reordering every other binary it manages.
func MiseGHDir() (string, error) {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(data, "gh-autoaccount", "bin"), nil
}

// MiseGHPath returns the gh shim inside MiseGHDir.
func MiseGHPath() (string, error) {
	dir, err := MiseGHDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gh"), nil
}

// CheckMiseGHReplaceable rejects an unmanaged shim path, or a directory that
// holds anything besides the shim, before setup mutates anything.
func CheckMiseGHReplaceable() error {
	path, err := MiseGHPath()
	if err != nil {
		return err
	}
	if err := checkMiseGHDirPure(filepath.Dir(path)); err != nil {
		return err
	}
	return ensureManagedOrMissing(path)
}

// CheckMiseGHLayout refuses a gh-only directory, or its parent, that is a
// symlink. Install and rollback create and prune real directories, and must not
// replace someone's symlinked layout with a physical one.
func CheckMiseGHLayout() error {
	dir, err := MiseGHDir()
	if err != nil {
		return err
	}
	for _, path := range []string{dir, filepath.Dir(dir)} {
		if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to use %s: it is a symlink; the gh-only directory must be a real directory", path)
		}
	}
	return nil
}

// checkMiseGHDirPure enforces that the whole directory can safely sit ahead of
// mise's tools. Install's transient temp directories are not commands, so a
// leftover from an interrupted run is tolerated, but only as a directory.
func checkMiseGHDirPure(dir string) error {
	if err := CheckMiseGHLayout(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect gh-only directory: %w", err)
	}
	for _, entry := range entries {
		if name := entry.Name(); name != "gh" && !(entry.IsDir() && strings.HasPrefix(name, ".gh-autoprofile-")) {
			return fmt.Errorf("refusing to use %s as the gh-only directory: it also contains %q", dir, name)
		}
	}
	return nil
}

// IsMiseGHPath reports whether path is, or resolves to, the gh-only shim
// location, whether or not the link is still owned. Nothing else belongs there,
// so a stale link must never be taken for a real gh.
func IsMiseGHPath(path string) bool {
	shim, err := MiseGHPath()
	if err != nil {
		return false
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		if shimDir, err := filepath.EvalSymlinks(filepath.Dir(shim)); err == nil &&
			filepath.Join(dir, filepath.Base(path)) == filepath.Join(shimDir, filepath.Base(shim)) {
			return true
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	shimInfo, err := os.Stat(shim)
	return err == nil && os.SameFile(info, shimInfo)
}

// IsMiseGHInstalled reports whether the gh-only shim is a managed, working link.
func IsMiseGHInstalled() bool {
	path, err := MiseGHPath()
	return err == nil && workingNativeLink(path)
}

// InstallMiseGH installs the gh-only shim with the same ownership rules as
// InstallNativeGH.
func InstallMiseGH(extensionBinaryPath string) (string, error) {
	path, err := MiseGHPath()
	if err != nil {
		return "", err
	}
	if err := checkMiseGHDirPure(filepath.Dir(path)); err != nil {
		return "", err
	}
	return installNativeLink(path, extensionBinaryPath)
}

// RemoveMiseGH removes the gh-only shim, and its directories once empty, and
// reports whether it removed one. An unmanaged path is never touched.
func RemoveMiseGH() (bool, error) {
	path, err := MiseGHPath()
	if err != nil {
		return false, err
	}
	if err := CheckMiseGHLayout(); err != nil {
		return false, err
	}
	if !managedNativeLink(path) {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	// Each fails harmlessly while the directory holds anything else.
	os.Remove(filepath.Dir(path))
	os.Remove(filepath.Dir(filepath.Dir(path)))
	return true, nil
}

// IsManagedGHShim reports whether path, as found on PATH, is one of the managed
// native gh shims. Directories are compared after resolving symlinks so a PATH
// entry reached through a symlinked parent still matches.
func IsManagedGHShim(path string) bool {
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return false
	}
	candidate := filepath.Join(dir, filepath.Base(path))
	for _, locate := range []func() (string, error){GHPath, MiseGHPath} {
		shim, err := locate()
		if err != nil || !managedNativeLink(shim) {
			continue
		}
		shimDir, err := filepath.EvalSymlinks(filepath.Dir(shim))
		if err == nil && candidate == filepath.Join(shimDir, filepath.Base(shim)) {
			return true
		}
	}
	return false
}

// siblingTargets returns the targets of the other managed native shims. They
// share one ownership record, so rewriting it must not orphan them.
func siblingTargets(path string) []string {
	var targets []string
	for _, locate := range []func() (string, error){GHPath, MiseGHPath} {
		other, err := locate()
		if err != nil || other == path || !managedNativeLink(other) {
			continue
		}
		if target, err := os.Readlink(other); err == nil {
			targets = append(targets, target)
		}
	}
	return targets
}

func appendUnique(list []string, values ...string) []string {
	for _, value := range values {
		if !slices.Contains(list, value) {
			list = append(list, value)
		}
	}
	return list
}

func installNativeLink(path, extensionBinaryPath string) (string, error) {
	if !NativeGHSupported() {
		return "", fmt.Errorf("native gh shim is unsupported on %s", runtime.GOOS)
	}
	if !filepath.IsAbs(extensionBinaryPath) {
		return "", fmt.Errorf("extension binary path must be absolute")
	}
	info, err := os.Stat(extensionBinaryPath)
	if err != nil {
		return "", fmt.Errorf("cannot inspect extension binary: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("extension binary is not executable")
	}
	if filepath.Clean(extensionBinaryPath) == filepath.Clean(path) {
		return "", fmt.Errorf("extension binary is the gh shim path")
	}
	resolved, err := filepath.EvalSymlinks(extensionBinaryPath)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) == filepath.Clean(path) {
		return "", fmt.Errorf("extension binary is the gh shim path")
	}
	extensionBinaryPath = resolved
	if other, err := os.Stat(path); err == nil && os.SameFile(info, other) && !managedNativeLink(path) {
		return "", fmt.Errorf("extension binary is the gh shim path")
	}
	if err := ensureManagedOrMissing(path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	record, err := nativeRecordPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(record), 0700); err != nil {
		return "", err
	}
	siblings := siblingTargets(path)
	targets := appendUnique([]string{extensionBinaryPath}, siblings...)
	if managedNativeLink(path) {
		old, _ := os.Readlink(path)
		targets = appendUnique(targets, old)
	}
	data, err := json.Marshal(targets)
	if err != nil {
		return "", err
	}
	if err := config.WriteAtomicMode(record, data, 0600); err != nil {
		return "", fmt.Errorf("cannot record native shim ownership: %w", err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(path), ".gh-autoprofile-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	link := filepath.Join(tmp, "gh")
	if err := os.Symlink(extensionBinaryPath, link); err != nil {
		return "", err
	}
	if err := os.Rename(link, path); err != nil {
		return "", fmt.Errorf("cannot install native gh shim: %w", err)
	}
	data, _ = json.Marshal(appendUnique([]string{extensionBinaryPath}, siblings...))
	if err := config.WriteAtomicMode(record, data, 0600); err != nil {
		return "", fmt.Errorf("native shim installed but cannot finalize ownership record: %w", err)
	}
	return path, nil
}
