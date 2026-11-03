package cmd

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/spf13/cobra"
)

func NewDefaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "default [user]", Short: "Show or set the default GitHub account", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			unset, _ := cmd.Flags().GetBool("unset")
			if unset && len(args) != 0 {
				return fmt.Errorf("--unset cannot be combined with a user")
			}
			if len(args) == 1 && (args[0] == "" || strings.IndexFunc(args[0], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0) {
				return fmt.Errorf("user must not be empty or contain whitespace or control characters")
			}
			if !unset && len(args) == 0 {
				settings, err := config.LoadSettings()
				if err != nil {
					return err
				}
				if settings.DefaultUser == "" {
					cmd.Println("(none)")
				} else {
					cmd.Println(settings.DefaultUser)
				}
				return nil
			}
			unlock, err := config.LockMutations()
			if err != nil {
				return err
			}
			defer unlock()
			settings, err := config.LoadSettings()
			if err != nil {
				return err
			}
			if unset {
				settings.DefaultUser = ""
			} else {
				if err := ghauth.ValidateUser(args[0]); err != nil {
					return err
				}
				settings.DefaultUser = args[0]
			}
			return config.SaveSettings(settings)
		},
	}
	cmd.Flags().Bool("unset", false, "Clear the default account")
	return cmd
}
