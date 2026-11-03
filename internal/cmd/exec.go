package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/profileexec"
	"github.com/spf13/cobra"
)

type execContext struct {
	Dir          string
	Pin          *config.Pin
	Token        string
	SelectedUser string
	SelectedHost string
}

// NewExecCmd creates the `exec` subcommand.
//
// Usage:
//
//	gh autoaccount exec -- git push
//	gh autoaccount exec -- gh pr list
//	gh autoaccount exec -- git status
//
// It resolves the pin for the current working directory, fetches the
// associated token, and runs the given command with the selected host-class
// token pair and profile markers injected into its environment.
// The child process's exit code is forwarded.
func NewExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec -- <command> [args...]",
		Short: "Run a command with the pinned account's token injected",
		Long: `Resolve the pin for the current directory, fetch the token, and run
the given command with the selected host-class token pair and profile markers
injected into its environment. Environment variables are not exact-host scoped.

Useful for non-interactive contexts (systemd timers, cron jobs, scripts)
where direnv and shell wrappers are not available.

--require-pin requires a directory pin; --user cannot replace it.
--expect-user checks the selected profile name (case-insensitive), not the
remote token identity. --verify-identity checks the credential remotely before
starting the child. --user without --require-pin selects a github.com account;
--dir also sets the child command's working directory.

Examples:
  gh autoaccount exec -- git push
	gh autoaccount exec --dir ~/repo -- gh pr list
	gh autoaccount exec --user alice -- git status
	gh autoaccount exec --require-pin --expect-user alice --verify-identity -- gh pr list`,
		Args:         cobra.MinimumNArgs(1),
		SilenceUsage: true,
		RunE:         runExec,
	}
	cmd.Flags().String("dir", "", "Resolve the pin for this directory instead of the current working directory")
	cmd.Flags().String("user", "", "Use this GitHub user directly instead of resolving a pin")
	cmd.Flags().Bool("require-pin", false, "Require a directory pin, even with --user")
	cmd.Flags().String("expect-user", "", "Require this selected profile (case-insensitive), not a remote identity check")
	cmd.Flags().Bool("verify-identity", false, "Verify the selected token identity before execution")
	return cmd
}

func runExec(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("usage: gh autoaccount exec -- <command> [args...]")
	}

	dirFlag, err := cmd.Flags().GetString("dir")
	if err != nil {
		return err
	}
	userFlag, err := cmd.Flags().GetString("user")
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("user") && userFlag == "" {
		return fmt.Errorf("--user must not be empty")
	}
	requirePin, err := cmd.Flags().GetBool("require-pin")
	if err != nil {
		return err
	}
	expectUser, err := cmd.Flags().GetString("expect-user")
	if err != nil {
		return err
	}
	if cmd.Flags().Changed("expect-user") && expectUser == "" {
		return fmt.Errorf("--expect-user must not be empty")
	}

	cwd := dirFlag
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot get working directory: %w", err)
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("cannot resolve directory %s: %w", cwd, err)
	}

	ctx, err := resolveExecContextWithPolicy(cwd, userFlag, requirePin, expectUser)
	if err != nil {
		return err
	}
	verifyIdentity, err := cmd.Flags().GetBool("verify-identity")
	if err != nil {
		return err
	}
	if verifyIdentity {
		if ctx.SelectedUser == "" {
			return fmt.Errorf("--verify-identity requires a selected profile")
		}
		if err := ghauth.VerifyTokenIdentity(cmd.Context(), ctx.SelectedUser, ctx.SelectedHost, ctx.Token); err != nil {
			return err
		}
	}

	argv := append([]string(nil), args...)
	argv[0], err = resolveExecCommandPath(argv[0])
	if err != nil {
		return err
	}

	env, err := buildExecEnv(ctx)
	if err != nil {
		return err
	}
	return profileexec.Execute(cmd.Context(), ctx.Dir, argv, env, os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr())
}

func resolveExecContext(cwd, overrideUser string) (*execContext, error) {
	return resolveExecContextWithPolicy(cwd, overrideUser, false, "")
}

func resolveExecContextWithPolicy(cwd, overrideUser string, requirePin bool, expectUser string) (*execContext, error) {
	ctx, err := profileexec.ResolveContextWithPolicy(cwd, profileexec.Policy{User: overrideUser, RequirePin: requirePin, ExpectUser: expectUser})
	if err != nil {
		return nil, err
	}
	return &execContext{Dir: ctx.Dir, Pin: ctx.Pin, Token: ctx.Token, SelectedUser: ctx.SelectedUser, SelectedHost: ctx.SelectedHost}, nil
}

func resolveExecCommandPath(command string) (string, error) {
	if filepath.Base(command) == "gh" {
		path, err := ghauth.ResolveGHPath()
		if err != nil {
			return "", fmt.Errorf("cannot resolve real gh binary: %w", err)
		}
		if filepath.Base(path) == path {
			path, err = exec.LookPath(path)
			if err != nil {
				return "", err
			}
		}
		return filepath.Abs(path)
	}
	if filepath.Base(command) != command {
		return command, nil
	}
	path, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("command not found: %s", command)
	}
	return filepath.Abs(path)
}

// setEnv sets or replaces an environment variable in a []string env slice.
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, entry := range env {
		if len(entry) >= len(prefix) && entry[:len(prefix)] == prefix {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func buildExecEnv(ctx *execContext) ([]string, error) {
	return profileexec.BuildExecEnv(&profileexec.Context{Dir: ctx.Dir, Pin: ctx.Pin, Token: ctx.Token, SelectedUser: ctx.SelectedUser, SelectedHost: ctx.SelectedHost})
}
