package shims

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

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
	if err != nil || !managedNativeLink(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0
}

// InstallNativeGH installs an owned symlink without recording a real gh path.
func InstallNativeGH(extensionBinaryPath string) (string, error) {
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
	path, err := GHPath()
	if err != nil {
		return "", err
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
	targets := []string{extensionBinaryPath}
	if managedNativeLink(path) {
		old, _ := os.Readlink(path)
		if old != extensionBinaryPath {
			targets = append(targets, old)
		}
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
	data, _ = json.Marshal([]string{extensionBinaryPath})
	if err := config.WriteAtomicMode(record, data, 0600); err != nil {
		return "", fmt.Errorf("native shim installed but cannot finalize ownership record: %w", err)
	}
	return path, nil
}
