//go:build linux || darwin

package skills

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func checkInstallPlatform() error { return nil }

func openExisting(root *os.Root) (*os.File, error) {
	before, err := root.Lstat("SKILL.md")
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular SKILL.md")
	}
	f, err := root.OpenFile("SKILL.md", os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	current, err := root.Lstat("SKILL.md")
	if err != nil || !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(before, opened) || !os.SameFile(opened, current) {
		f.Close()
		return nil, fmt.Errorf("refusing changed or non-regular SKILL.md")
	}
	return f, nil
}
