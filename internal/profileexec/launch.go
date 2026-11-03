package profileexec

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

type replacementKey struct{}

// WithProcessReplacement enables process replacement at the CLI boundary.
func WithProcessReplacement(ctx context.Context) context.Context {
	return context.WithValue(ctx, replacementKey{}, true)
}

// Execute runs a command, optionally replacing the CLI process on supported systems.
func Execute(ctx context.Context, dir string, argv, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(argv) == 0 {
		return fmt.Errorf("no command provided")
	}
	if ctx.Value(replacementKey{}) == true && stdin == os.Stdin && stdout == os.Stdout && stderr == os.Stderr {
		return replaceProcess(ctx, dir, argv, env)
	}
	return runProcess(ctx, dir, argv, env, stdin, stdout, stderr)
}

func runProcess(ctx context.Context, dir string, argv, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
	path := argv[0]
	if filepath.Base(path) == path {
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
	cmd := exec.CommandContext(ctx, path, argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, env, stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return ExitError{code: processExitCode(exit)}
		}
		return err
	}
	return nil
}
