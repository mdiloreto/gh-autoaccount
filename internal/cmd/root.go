package cmd

import (
	"fmt"
	"os"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
	"github.com/spf13/cobra"
)

// Version is set at build time via ldflags.
var Version = "dev"

// NewRootCmd creates the top-level cobra command for gh-autoaccount.
func NewRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gh-autoaccount",
		Short: "Automatic GitHub profile switching per directory",
		Long: `gh-autoaccount pins a GitHub account to a directory, selecting the
right profile context and git identity without manual switching.

Quick start:
  gh autoaccount setup --native                  # one-time setup
  gh autoaccount pin <user> --dir <path>         # pin an account
  cd <path> && gh api /user --jq .login          # verify it works`,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			subcmd := ""
			if len(os.Args) > 1 {
				subcmd = os.Args[1]
			}
			if subcmd == "setup" || subcmd == "doctor" || subcmd == "help" || subcmd == "completion" || subcmd == "credential-helper" || subcmd == "exec" || subcmd == "__shim-gh" || subcmd == "__git-credential" {
				return nil
			}
			warnUpgradeDrift(cmd)
			return nil
		},
	}

	cmd.AddCommand(
		NewSetupCmd(),
		NewExecCmd(),
		NewDefaultCmd(),
		NewGitCredentialCmd(),
		NewPinCmd(),
		NewUnpinCmd(),
		NewListCmd(),
		NewStatusCmd(),
		NewDoctorCmd(),
		NewCredentialHelperCmd(),
		NewShimGHCMD(),
	)

	return cmd
}

func warnUpgradeDrift(cmd *cobra.Command) {
	registry, err := config.LoadPins()
	if err != nil {
		return
	}

	settings, err := config.LoadSettings()
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "gh-autoaccount: cannot load setup settings: %v\n", err)
		return
	}
	needsSetup := !direnvlib.IsShellLibInstalled() || (!settings.ExternalShell && !direnvlib.CheckShellHookInstalled())
	needsModeMigration := false

	for _, pin := range registry.Pins {
		if pin.Mode == "" {
			needsModeMigration = true
		}
	}

	if !(needsSetup || needsModeMigration) {
		return
	}

	fmt.Fprintln(cmd.ErrOrStderr(), "gh-autoaccount: upgrade tasks detected. Run `gh autoaccount setup --migrate` to apply security migrations.")
}
