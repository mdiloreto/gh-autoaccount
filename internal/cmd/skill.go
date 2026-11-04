package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/skills"
	"github.com/spf13/cobra"
)

func NewSkillCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "skill", Short: "Print the bundled agent skill", Args: cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			s := skills.Content()
			n, err := io.WriteString(cmd.OutOrStdout(), s)
			if err == nil && n != len(s) {
				return io.ErrShortWrite
			}
			return err
		},
	}
	var dir string
	install := &cobra.Command{
		Use: "install", Short: "Install the bundled skill under an explicit skills parent",
		Long: "On Linux and macOS, install under --dir/gh-autoaccount/SKILL.md without overwriting existing content. The selected parent and its ancestors may be symlinks; the managed directory and file may not. Example: --dir .agents/skills",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(dir) == "" {
				return fmt.Errorf("--dir must not be blank")
			}
			path, installed, err := skills.Install(dir)
			if err != nil {
				return err
			}
			action := "Already installed"
			if installed {
				action = "Installed"
			}
			s := fmt.Sprintf("%s: %s\n", action, path)
			n, err := io.WriteString(cmd.OutOrStdout(), s)
			if err == nil && n != len(s) {
				return io.ErrShortWrite
			}
			return err
		},
	}
	install.Flags().StringVar(&dir, "dir", "", "skills parent directory (required; no path expansion)")
	_ = install.MarkFlagRequired("dir")
	cmd.AddCommand(install)
	return cmd
}
