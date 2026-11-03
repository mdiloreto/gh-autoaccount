package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/profileexec"
	"github.com/spf13/cobra"
)

// NewShimGHCMD creates the hidden gh shim dispatcher.
func NewShimGHCMD() *cobra.Command {
	return &cobra.Command{
		Use:                "__shim-gh",
		Hidden:             true,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			realGH, err := ghauth.ResolveGHPath()
			if err != nil {
				return err
			}
			argv := append([]string{realGH}, args...)
			if os.Getenv("GH_AUTOPROFILE_DISABLE_SHIM") != "" {
				return profileexec.Execute(cmd.Context(), ".", argv, os.Environ(), os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			if len(args) > 0 && (args[0] == "autoaccount" || args[0] == "aa" && hasShortAlias(cmd.Context(), realGH)) {
				target, err := managementExtensionPath()
				if err != nil {
					return err
				}
				env := setEnv(os.Environ(), "GH_EXTENSION", "1")
				env = setEnv(env, "GH_PATH", realGH)
				argv := append([]string{target}, args[1:]...)
				return profileexec.Execute(cmd.Context(), ".", argv, env, os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			if shouldBypassAuthInjection(args) {
				return profileexec.Execute(cmd.Context(), ".", argv, os.Environ(), os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			ctx, err := profileexec.ResolveContext(".", false)
			if err != nil {
				return err
			}
			env, err := profileexec.BuildEnv(ctx, false)
			if err != nil {
				return err
			}
			return profileexec.Execute(cmd.Context(), ctx.Dir, argv, env, os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func shouldBypassAuthInjection(args []string) bool {
	if len(args) < 2 || args[0] != "auth" {
		return false
	}
	switch args[1] {
	case "switch", "login", "logout", "refresh", "setup-git":
		return true
	default:
		return false
	}
}

func managementExtensionPath() (string, error) {
	if runtime.GOOS == "windows" {
		return "", fmt.Errorf("management extension dispatch is unavailable on Windows")
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot locate gh extension directory: %w", err)
		}
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("cannot locate gh extension: HOME must be an absolute path")
		}
		data = filepath.Join(home, ".local", "share")
	} else if !filepath.IsAbs(data) {
		return "", fmt.Errorf("cannot locate gh extension: XDG_DATA_HOME must be an absolute path")
	}
	target := filepath.Join(data, "gh", "extensions", "gh-autoaccount", "gh-autoaccount")
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", fmt.Errorf("gh-autoaccount extension is not installed or executable at %s; install or repair the extension", target)
	}
	return target, nil
}
