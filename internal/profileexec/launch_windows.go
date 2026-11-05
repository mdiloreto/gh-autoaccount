//go:build windows

package profileexec

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
)

// replaceProcess runs the child and waits, since Windows has no exec. The console
// delivers Ctrl+C to every attached process, so this process ignores it and
// lets the child decide, instead of exiting and orphaning it.
func replaceProcess(ctx context.Context, dir string, argv, env []string) error {
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	return runProcess(ctx, dir, argv, env, os.Stdin, os.Stdout, os.Stderr)
}

func processExitCode(err *exec.ExitError) int { return err.ExitCode() }
