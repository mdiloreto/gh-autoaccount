package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/profileexec"
	"github.com/spf13/cobra"
)

// NewListCmd creates the `list` subcommand.
func NewListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list",
		Aliases:           []string{"ls"},
		Short:             "List all pinned directories and owners",
		Long:              `Show a table of all directories and repository owners with pinned GitHub accounts.`,
		RunE:              runList,
		PersistentPreRunE: diagnosticsPreRun,
	}
	cmd.Flags().Bool("json", false, "Output versioned JSON")
	return cmd
}

func runList(cmd *cobra.Command, args []string) error {
	registry, err := config.LoadPins()
	if err != nil {
		return diagnosticConfigError("pin registry", "pins.yml", err)
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		pins := make([]pinDiagnostics, 0, len(registry.Pins))
		for i := range registry.Pins {
			pins = append(pins, *describePin(&registry.Pins[i]))
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
			SchemaVersion int              `json:"schema_version"`
			Pins          []pinDiagnostics `json:"pins"`
		}{SchemaVersion: 1, Pins: pins})
	}
	out := cmd.OutOrStdout()

	if len(registry.Pins) == 0 {
		fmt.Fprintln(out, "No pinned directories.")
		fmt.Fprintln(out, "Pin one with: gh autoaccount pin <username> --dir <path>")
		return nil
	}

	// Selection errors must not prevent listing the registry.
	var selected *config.Pin
	if cwd, err := os.Getwd(); err == nil {
		selected, _, _ = profileexec.SelectPin(registry, cwd, &profileexec.RepoQuery{GH: true})
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "DIRECTORY\tACCOUNT\tHOST\tTOKEN COMMAND CONFIGURED\tMODE\tGIT EMAIL\tGIT NAME\tSSH KEY")
	fmt.Fprintln(w, "---------\t-------\t----\t------------------------\t----\t---------\t--------\t-------")

	for i := range registry.Pins {
		pin := &registry.Pins[i]
		marker := " "
		if pin == selected {
			marker = "*"
		}

		mode := string(pin.EffectiveMode())
		email := pin.GitEmail
		if email == "" {
			email = "-"
		}
		name := pin.GitName
		if name == "" {
			name = "-"
		}
		sshKey := pin.SSHKey
		if sshKey == "" {
			sshKey = "-"
		}

		description := describePin(pin)
		target := pin.Dir
		if pin.IsOwner() {
			target = "owner:" + pin.Owner
		}
		fmt.Fprintf(w, "%s %s\t%s\t%s\t%t\t%s\t%s\t%s\t%s\n", marker, target, pin.User, description.Host, description.TokenCommandConfigured, mode, email, name, sshKey)
	}

	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%d pin(s) total. (* = selected pin)\n", len(registry.Pins))

	return nil
}
