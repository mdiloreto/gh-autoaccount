package cmd

import (
	"fmt"

	"github.com/mdiloreto/gh-autoaccount/internal/profileexec"
	"github.com/spf13/cobra"
)

// NewCurrentCmd creates the `current` subcommand.
func NewCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "current",
		Short: "Print the account plain gh uses here, for shell prompts",
		Long: `Print only the login plain gh would use in the current directory, or nothing
when no account is selected. It resolves no credential and makes no network call,
so it is cheap enough for a shell prompt or status line. It follows the same
selection as plain gh: the session override (GH_AUTOACCOUNT_USER), an owner pin
for the checkout's remote, the deepest directory pin, then the default.

Example (zsh): PROMPT='$(gh autoaccount current) %~ %# '`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			user, _, err := profileexec.SelectedAccount(".")
			if err != nil {
				return err
			}
			if user != "" {
				fmt.Fprintln(cmd.OutOrStdout(), sanitizeStatusValue(user))
			}
			return nil
		},
	}
}
