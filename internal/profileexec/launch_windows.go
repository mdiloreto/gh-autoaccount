//go:build windows

package profileexec

import (
	"context"
	"os"
	"os/exec"
)

func replaceProcess(ctx context.Context, dir string, argv, env []string) error {
	return runProcess(ctx, dir, argv, env, os.Stdin, os.Stdout, os.Stderr)
}

func processExitCode(err *exec.ExitError) int { return err.ExitCode() }
