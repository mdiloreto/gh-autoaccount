package cmd

import (
	"bufio"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// NewCredentialHelperCmd creates the `credential-helper` subcommand.
// It implements the git credential helper protocol so that git can
// resolve the correct GitHub token based on the working directory's
// pin, without requiring direnv or shell wrappers.
//
// Usage in gitconfig:
//
//	[credential "https://github.com"]
//	    helper =
//	    helper = !/path/to/gh-autoaccount credential-helper
func NewCredentialHelperCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "credential-helper [get|store|erase]",
		Short:  "Git credential helper — resolves tokens from pins.yml",
		Long:   `Implements the git credential helper protocol. Resolves the correct GitHub token based on the working directory's pin registry, without requiring direnv or shell wrappers. Intended for non-interactive contexts (systemd, cron, IDEs).`,
		Hidden: false,
		Args:   cobra.MaximumNArgs(1),
		RunE:   runCredentialHelper,
		// Suppress the upgrade-drift warning for this subcommand — it runs
		// in a non-interactive git credential pipeline where stderr noise
		// would break the protocol.
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
}

// credentialFields holds the key=value pairs from git's credential protocol.
type credentialFields struct {
	Protocol string
	Host     string
	Path     string
}

func runCredentialHelper(cmd *cobra.Command, args []string) error {
	// Only respond to the "get" action. For "store" and "erase" we exit
	// silently — we don't persist or revoke tokens.
	action := ""
	if len(args) > 0 {
		action = args[0]
	}
	if action != "get" {
		return nil
	}

	// Parse credential fields from stdin.
	req, err := readCredentialRequest(os.Stdin)
	if err != nil {
		_, _ = io.WriteString(os.Stdout, "quit=true\n\n")
		return err
	}

	// Only handle HTTPS requests to github.com (or GitHub Enterprise hosts
	// in the future). Exit silently for anything else so git falls through
	// to the next credential helper.
	return handleGitCredentialGet(req)
}

// parseCredentialInput reads key=value pairs from a reader until a blank
// line or EOF, per the git credential helper protocol.
func parseCredentialInput(r io.Reader) credentialFields {
	var fields credentialFields
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, value := parts[0], parts[1]
		switch key {
		case "protocol":
			fields.Protocol = value
		case "host":
			fields.Host = value
		case "path":
			fields.Path = value
		}
	}
	return fields
}
