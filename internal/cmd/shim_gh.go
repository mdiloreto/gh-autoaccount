package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
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
			ctx, err := profileexec.ResolveContextWithPolicy(".", profileexec.Policy{Repo: &profileexec.RepoQuery{GH: true, Args: args}})
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
	data, err := ghDataDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate gh extension: %w", err)
	}
	target := filepath.Join(data, "extensions", "gh-autoaccount", config.ExeName("gh-autoaccount"))
	info, err := os.Stat(target)
	if err != nil || !config.IsExecutable(info) {
		return "", fmt.Errorf("gh-autoaccount extension is not installed or executable at %s; install or repair the extension", target)
	}
	return target, nil
}

// ghDataDir mirrors gh's own data directory lookup, which is where gh
// installs extensions.
func ghDataDir() (string, error) {
	for _, key := range []string{"GH_DATA_DIR", "XDG_DATA_HOME"} {
		dir := os.Getenv(key)
		if dir == "" {
			continue
		}
		if !filepath.IsAbs(dir) {
			return "", fmt.Errorf("%s must be an absolute path", key)
		}
		if key == "XDG_DATA_HOME" {
			dir = filepath.Join(dir, "gh")
		}
		return dir, nil
	}
	if local := os.Getenv("LOCALAPPDATA"); runtime.GOOS == "windows" && local != "" {
		if !filepath.IsAbs(local) {
			return "", fmt.Errorf("LOCALAPPDATA must be an absolute path")
		}
		return filepath.Join(local, "GitHub CLI"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("HOME must be an absolute path")
	}
	return filepath.Join(home, ".local", "share", "gh"), nil
}
