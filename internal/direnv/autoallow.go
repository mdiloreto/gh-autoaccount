package direnv

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

// AllowSnapshot implements the private allow-store format of direnv 2.38.2.
// It binds authorization, not direnv's later check-to-execution sequence.
func AllowSnapshot(dir string, data []byte) error {
	version, err := GetVersion()
	if err != nil || version != "2.38.2" {
		return fmt.Errorf("unsupported direnv version %q: %v", version, err)
	}
	if os.Getenv("DIRENV_REQUIRED") != "" {
		return fmt.Errorf("DIRENV_REQUIRED needs manual authorization")
	}
	if HasUnmanagedContent(data) {
		return fmt.Errorf("unmanaged content needs manual authorization")
	}
	path := filepath.Join(dir, ".envrc")
	if !filepath.IsAbs(path) {
		return fmt.Errorf("envrc path is not absolute")
	}
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("envrc path contains symlinks or cannot be resolved: %s", path)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("envrc is not a safe regular file")
	}
	if err := verifyLocalMetadata(path, info, false); err != nil {
		return err
	}
	if err := safeDirectoryComponents(filepath.Dir(path)); err != nil {
		return err
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || dirInfo.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("unsafe checkout directory")
	}
	if err := verifyLocalMetadata(filepath.Dir(path), dirInfo, false); err != nil {
		return err
	}
	home := os.Getenv("HOME")
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	conf := os.Getenv("DIRENV_CONFIG")
	if conf == "" {
		conf = filepath.Join(configHome, "direnv")
	}
	if !filepath.IsAbs(dataHome) || !filepath.IsAbs(conf) {
		return fmt.Errorf("direnv data and config paths must be absolute")
	}
	if err := safeDirectoryComponents(conf); err != nil {
		return err
	}
	// Whitelists or other config can make a file executable independently of
	// the content allow entry. Do not claim a safe auto-allow in that case.
	for _, name := range []string{"direnv.toml", "config.toml"} {
		if _, err := os.Lstat(filepath.Join(conf, name)); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("direnv configuration requires manual review")
		}
	}
	root := filepath.Join(dataHome, "direnv")
	allowDir := filepath.Join(root, "allow")
	if _, err := os.Lstat(allowDir); os.IsNotExist(err) {
		if _, legacyErr := os.Lstat(filepath.Join(conf, "allow")); legacyErr == nil || !os.IsNotExist(legacyErr) {
			return fmt.Errorf("legacy direnv allow store requires manual migration")
		}
	} else if err != nil {
		return err
	}
	// Inspect every existing ancestor before creating anything. In particular,
	// MkdirAll must not follow a pre-existing symlink into another tree.
	if err := safeDirectoryComponents(allowDir); err != nil {
		return err
	}
	if err := os.MkdirAll(allowDir, 0700); err != nil {
		return err
	}
	for _, p := range []string{root, allowDir} {
		r, err := filepath.EvalSymlinks(p)
		if err != nil || r != filepath.Clean(p) {
			return fmt.Errorf("unsafe direnv data path: %s", p)
		}
		st, err := os.Lstat(p)
		if err != nil || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe direnv data directory: %s", p)
		}
		if err := verifyLocalMetadata(p, st, false); err != nil {
			return err
		}
	}
	pathHash := sha256.Sum256([]byte(path + "\n"))
	denyDir := filepath.Join(root, "deny")
	if st, err := os.Lstat(denyDir); err == nil {
		if !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe direnv deny directory")
		}
		if err := verifyLocalMetadata(denyDir, st, false); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	deny := filepath.Join(denyDir, fmt.Sprintf("%x", pathHash))
	if _, err := os.Lstat(deny); err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("direnv deny exists or cannot be inspected")
	}
	h := sha256.New()
	h.Write([]byte(path + "\n"))
	h.Write(data)
	entry := filepath.Join(allowDir, fmt.Sprintf("%x", h.Sum(nil)))
	f, err := os.CreateTemp(allowDir, ".gh-autoprofile-allow-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(path + "\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), entry)
}

func safeDirectoryComponents(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		st, statErr := os.Lstat(p)
		if statErr == nil {
			if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || (st.Mode().Perm()&0022 != 0 && st.Mode()&os.ModeSticky == 0) {
				return fmt.Errorf("unsafe data path component: %s", p)
			}
			if err := verifyLocalMetadata(p, st, true); err != nil {
				return err
			}
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
