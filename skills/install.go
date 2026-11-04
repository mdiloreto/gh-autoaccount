package skills

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func install(dir, content string) (path string, installed bool, err error) {
	if err := checkInstallPlatform(); err != nil {
		return "", false, err
	}
	if strings.TrimSpace(dir) == "" {
		return "", false, errors.New("skills parent directory must not be blank")
	}
	path = filepath.Join(dir, "gh-autoaccount", "SKILL.md")
	if err = os.MkdirAll(dir, 0755); err != nil {
		return path, false, err
	}
	parent, err := os.OpenRoot(dir)
	if err != nil {
		return path, false, err
	}
	defer parent.Close()
	if err = parent.Mkdir("gh-autoaccount", 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return path, false, err
	}
	info, err := parent.Lstat("gh-autoaccount")
	if err != nil {
		return path, false, err
	}
	if !info.IsDir() {
		return path, false, fmt.Errorf("refusing non-directory or symlink: %s", filepath.Dir(path))
	}
	root, err := parent.OpenRoot("gh-autoaccount")
	if err != nil {
		return path, false, err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return path, false, err
	}
	current, err := parent.Lstat("gh-autoaccount")
	if err != nil || !current.IsDir() || !os.SameFile(info, opened) || !os.SameFile(opened, current) {
		return path, false, fmt.Errorf("refusing changed managed directory: %s", filepath.Dir(path))
	}
	_, err = root.Lstat("SKILL.md")
	if err == nil {
		return path, false, checkExisting(root, content)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return path, false, err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return path, false, err
	}
	temp := ".skill-" + hex.EncodeToString(random[:]) + ".tmp"
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return path, false, err
	}
	defer func() {
		if e := root.Remove(temp); e != nil {
			err = errors.Join(err, fmt.Errorf("remove temporary skill: %w", e))
		}
	}()
	n, writeErr := io.WriteString(f, content)
	if writeErr == nil && n != len(content) {
		writeErr = io.ErrShortWrite
	}
	if closeErr := f.Close(); closeErr != nil {
		writeErr = errors.Join(writeErr, closeErr)
	}
	if writeErr != nil {
		return path, false, writeErr
	}
	if err = root.Link(temp, "SKILL.md"); err != nil {
		if errors.Is(err, os.ErrExist) {
			return path, false, checkExisting(root, content)
		}
		return path, false, fmt.Errorf("publish skill without overwriting: %w", err)
	}
	return path, true, nil
}

func checkExisting(root *os.Root, content string) error {
	f, err := openExisting(root)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(len(content))+1))
	if err != nil {
		return err
	}
	if string(b) != content {
		return errors.New("existing SKILL.md differs from bundled skill; remove or move it before installing")
	}
	return nil
}
