//go:build !windows

package profileexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func replaceProcess(_ context.Context, dir string, argv, env []string) error {
	path := argv[0]
	if !strings.ContainsRune(path, os.PathSeparator) {
		var err error
		path, err = exec.LookPath(path)
		if err != nil {
			return err
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return err
		}
	}
	oldDir, err := os.Getwd()
	if err != nil {
		return err
	}
	if err = os.Chdir(dir); err != nil {
		return err
	}
	err = syscall.Exec(path, argv, env)
	if restoreErr := os.Chdir(oldDir); restoreErr != nil {
		return errors.Join(err, restoreErr)
	}
	return err
}

func processExitCode(err *exec.ExitError) int {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return err.ExitCode()
}
