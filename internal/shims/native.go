package shims

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

func NativeGHSupported() bool {
	return runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

// copyProxy selects a copied proxy instead of a symlink. Windows symlinks need
// administrator rights or Developer Mode; a copy needs neither. A variable so
// tests on other platforms can exercise the copy path.
var copyProxy = runtime.GOOS == "windows"

// copyPrefix marks a recorded copy digest in the ownership record, which
// otherwise lists symlink targets.
const copyPrefix = "sha256:"

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return copyPrefix + hex.EncodeToString(hash.Sum(nil)), nil
}

// proxyEntry returns the ownership record entry an installed proxy matches: its
// symlink target, or the digest of a copy.
func proxyEntry(path string) (string, error) {
	if copyProxy {
		return fileDigest(path)
	}
	return os.Readlink(path)
}

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
	if copyProxy {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		digest, err := fileDigest(path)
		return err == nil && slices.Contains(nativeTargets(), digest)
	}
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

// NativeGHStale reports whether a copied proxy no longer matches the installed
// extension binary, as happens after gh extension upgrade. A symlink follows
// the extension and is never stale.
func NativeGHStale(extensionBinaryPath string) bool {
	path, err := GHPath()
	if err != nil || !copyProxy {
		return false
	}
	installed, err := fileDigest(path)
	if err != nil {
		return false
	}
	current, err := fileDigest(extensionBinaryPath)
	return err == nil && installed != current
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
	return err == nil && config.IsExecutable(info)
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
		if err == nil && config.SamePath(candidate, filepath.Join(shimDir, filepath.Base(shim))) {
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
		if entry, err := proxyEntry(other); err == nil {
			targets = append(targets, entry)
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
	if !config.IsExecutable(info) {
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
	source := extensionBinaryPath
	if copyProxy {
		if source, err = fileDigest(extensionBinaryPath); err != nil {
			return "", fmt.Errorf("cannot read extension binary: %w", err)
		}
	}
	siblings := siblingTargets(path)
	targets := appendUnique([]string{source}, siblings...)
	// The old entry stays recorded until the new proxy is in place, so an
	// interrupted install still owns whichever proxy is on disk.
	if managedNativeLink(path) {
		if old, err := proxyEntry(path); err == nil {
			targets = appendUnique(targets, old)
		}
	}
	data, err := json.Marshal(targets)
	if err != nil {
		return "", err
	}
	if err := config.WriteAtomicMode(record, data, 0600); err != nil {
		return "", fmt.Errorf("cannot record native shim ownership: %w", err)
	}
	if copyProxy {
		if err := placeCopy(path, extensionBinaryPath); err != nil {
			return "", fmt.Errorf("cannot install native gh shim: %w", err)
		}
	} else {
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
	}
	data, _ = json.Marshal(appendUnique([]string{source}, siblings...))
	if err := config.WriteAtomicMode(record, data, 0600); err != nil {
		return "", fmt.Errorf("native shim installed but cannot finalize ownership record: %w", err)
	}
	return path, nil
}

// placeCopy replaces path with a copy of source. Windows refuses to overwrite or
// delete a running executable but allows renaming it, so an existing proxy is
// moved aside under a unique name first. Set-aside copies that are no longer
// running are removed on a later install; only names this function creates are
// ever removed.
func placeCopy(path, source string) error {
	dir := filepath.Dir(path)
	removeSetAsideCopies(dir)
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(dir, ".gh-autoprofile-*"+filepath.Ext(path))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0755); err != nil {
		return err
	}
	aside := ""
	if _, err := os.Lstat(path); err == nil {
		if aside, err = setAsideName(dir); err != nil {
			return err
		}
		if err := os.Rename(path, aside); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		if aside != "" {
			err = errors.Join(err, os.Rename(aside, path))
		}
		return err
	}
	if aside != "" {
		_ = os.Remove(aside)
	}
	return nil
}

const setAsidePrefix = ".gh-autoprofile-old-"

// setAsideName reserves an unused name for a proxy moved out of the way.
func setAsideName(dir string) (string, error) {
	f, err := os.CreateTemp(dir, setAsidePrefix+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", err
	}
	// Rename onto a just-created empty file replaces it on every platform.
	return name, nil
}

// removeSetAsideCopies deletes earlier set-aside proxies and temporary copies
// left by an interrupted install; a copy still running stays until a later one.
func removeSetAsideCopies(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".gh-autoprofile-") {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}
