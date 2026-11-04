//go:build !linux && !darwin

package skills

import (
	"errors"
	"os"
)

func checkInstallPlatform() error {
	return errors.New("skill installation is supported only on Linux and macOS")
}

func openExisting(root *os.Root) (*os.File, error) {
	return nil, errors.New("cannot safely inspect an existing SKILL.md on this platform")
}
