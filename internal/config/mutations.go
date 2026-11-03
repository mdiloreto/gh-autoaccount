package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// LockMutations covers settings/registry reads through identity publication and
// envrc updates. Fail rather than overwrite another command's newer snapshot.
func LockMutations() (func(), error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "mutation.lock")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("cannot lock onboarding state %s (%s; retry after other commands finish; remove a stale lock only when no command is running): %w", path, describeMutationLock(path), err)
		}
		return nil, fmt.Errorf("cannot create onboarding lock %s: %w", path, err)
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return func() { _ = os.Remove(path) }, nil
}

func describeMutationLock(path string) string {
	details := make([]string, 0, 2)
	if data, err := os.ReadFile(path); err == nil {
		pidText := strings.TrimSpace(string(data))
		if pid, err := strconv.Atoi(pidText); err == nil && pid > 0 {
			details = append(details, fmt.Sprintf("held by PID %d", pid))
		}
	}
	if info, err := os.Stat(path); err == nil {
		age := time.Since(info.ModTime()).Round(time.Second)
		if age < 0 {
			age = 0
		}
		details = append(details, fmt.Sprintf("age %s", age))
	}
	if len(details) == 0 {
		return "existing lock details unavailable"
	}
	return strings.Join(details, ", ")
}

func WriteAtomic(path string, data []byte) error {
	return WriteAtomicMode(path, data, 0600)
}

func WriteAtomicMode(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".autoprofile-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// WriteAtomicPreserveMode atomically updates a user-managed file without
// replacing an intentional symlink such as a dotfiles-managed shell RC.
func WriteAtomicPreserveMode(path string, data []byte, defaultMode os.FileMode) error {
	resolved := path
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
		info, err = os.Stat(resolved)
	}
	mode := defaultMode
	if err == nil {
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	return WriteAtomicMode(resolved, data, mode)
}
