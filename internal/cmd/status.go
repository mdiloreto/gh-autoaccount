package cmd

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
	"github.com/spf13/cobra"
)

// NewStatusCmd creates the `status` subcommand.
func NewStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the profile context for a directory",
		Long: `Display the selected directory's pinned account, token variables in the
environment, credential availability, and actual Git identities.
Uses local lookups only; remote GitHub identity and token scopes are not verified.`,
		RunE:              runStatus,
		PersistentPreRunE: diagnosticsPreRun,
	}
	cmd.Flags().String("dir", "", "Directory to inspect (default: current directory)")
	cmd.Flags().Bool("json", false, "Output versioned local-only JSON without network verification")
	return cmd
}

func runStatus(cmd *cobra.Command, args []string) error {
	settings, err := config.LoadSettings()
	if err != nil {
		return diagnosticConfigError("setup settings", "settings.yml", err)
	}
	dir, _ := cmd.Flags().GetString("dir")
	diagnostics, err := collectStatus(dir)
	if err != nil {
		return err
	}
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(diagnostics)
	}
	cmd.SetOut(cmd.OutOrStdout())

	cwd := diagnostics.Directory
	pin := diagnostics.Pin
	selectedUser := diagnostics.SelectedUser
	if selectedUser == "" {
		selectedUser = "(none)"
	}
	cmd.Printf("Selected account: %s (source: %s)\n", sanitizeStatusValue(selectedUser), sanitizeStatusValue(diagnostics.SelectionSource))
	cmd.Printf("Directory: %s\n", sanitizeStatusValue(cwd))
	cmd.Println()

	if len(diagnostics.ConflictingOwnerPins) > 0 {
		cmd.Printf("WARN: remotes of this checkout match owner pins (%s) that credentials may not follow.\n", sanitizeStatusValue(strings.Join(diagnostics.ConflictingOwnerPins, ", ")))
		cmd.Println("      Git identity matches every remote URL but credentials follow one selection, so they can disagree.")
		cmd.Println()
	}

	// Pinned account
	if pin != nil {
		cmd.Printf("  Pinned account:   %s\n", sanitizeStatusValue(pin.User))
		cmd.Printf("  Host:             %s\n", sanitizeStatusValue(pin.Host))
		cmd.Printf("  Token command configured: %t\n", pin.TokenCommandConfigured)
		if pin.Owner != "" {
			cmd.Printf("  Pinned owner:     %s\n", sanitizeStatusValue(pin.Owner))
		} else if pin.Directory != cwd {
			cmd.Printf("  Pinned directory: %s\n", sanitizeStatusValue(pin.Directory))
		}
		if pin.GitEmail != "" {
			cmd.Printf("  Pinned email:     %s\n", sanitizeStatusValue(pin.GitEmail))
		}
		if pin.GitName != "" {
			cmd.Printf("  Pinned name:      %s\n", sanitizeStatusValue(pin.GitName))
		}
		if pin.SSHKey != "" {
			cmd.Printf("  Pinned SSH key:   %s\n", sanitizeStatusValue(pin.SSHKey))
		}
	} else {
		cmd.Println("  Pinned account:   (none)")
	}
	cmd.Println()

	// Active environment
	ghTokenPresent := diagnostics.Environment.GHTokenPresent
	githubTokenPresent := diagnostics.Environment.GitHubTokenPresent
	enterpriseTokenPresent := diagnostics.Environment.GHEnterpriseTokenPresent || diagnostics.Environment.GitHubEnterpriseTokenPresent

	cmd.Println("  Environment:")
	cmd.Printf("    GH_TOKEN:             %s\n", presentLabel(ghTokenPresent))
	cmd.Printf("    GITHUB_TOKEN:         %s\n", presentLabel(githubTokenPresent))
	cmd.Printf("    GH_ENTERPRISE_TOKEN:  %s\n", presentLabel(diagnostics.Environment.GHEnterpriseTokenPresent))
	cmd.Printf("    GITHUB_ENTERPRISE_TOKEN: %s\n", presentLabel(diagnostics.Environment.GitHubEnterpriseTokenPresent))
	cmd.Println()
	if diagnostics.Authentication.Selected {
		cmd.Printf("  Selected credential: available=%t (source: %s)\n", diagnostics.Authentication.CredentialAvailable, sanitizeStatusValue(diagnostics.Authentication.Source))
		if diagnostics.Authentication.Error != nil {
			cmd.Printf("  Authentication: %s\n", sanitizeStatusValue(diagnostics.Authentication.Error.Message))
		}
	} else {
		cmd.Println("  Selected credential: (none; default gh behavior)")
	}
	cmd.Println("  GitHub identity:   not verified (local credential lookup does not verify the remote account)")
	cmd.Println("  Token scopes:      not inspected (local diagnostics only)")

	cmd.Println()
	cmd.Println("  Git (current configuration/environment, separate from GitHub authentication):")
	if diagnostics.Git.GitDir != "" {
		cmd.Printf("    Git directory:        %s\n", sanitizeStatusValue(diagnostics.Git.GitDir))
	}
	if diagnostics.Git.AuthorIdent != "" {
		cmd.Printf("    Author identity:      %s\n", sanitizeStatusValue(diagnostics.Git.AuthorIdent))
	}
	if diagnostics.Git.CommitterIdent != "" {
		cmd.Printf("    Committer identity:   %s\n", sanitizeStatusValue(diagnostics.Git.CommitterIdent))
	}
	if diagnostics.Git.Error != nil {
		cmd.Printf("    %s\n", sanitizeStatusValue(diagnostics.Git.Error.Message))
	}

	cmd.Println()
	cmd.Println("  Tooling:")
	if settings.ExternalShell {
		cmd.Println("    shell integration:    external, not verified (RC management disabled)")
	}
	if settings.GitIdentity {
		cmd.Printf("    Git identity include registered: %t\n", gitidentity.CheckRegistered())
		cmd.Println("    Git identity follows gitdir; local config and environment can override.")
	} else {
		cmd.Println("    managed Git identity: disabled (setup --git-identity to enable)")
	}
	if gh := diagnostics.GHOnPath; gh != nil {
		cmd.Printf("    gh on PATH:           %s\n", sanitizeStatusValue(gh.Path))
		if shims.IsNativeGHInstalled() && !gh.NativeShim {
			cmd.Println("    WARN: gh on PATH is not the managed shim; " + sanitizeStatusValue(shadowFixHint(gh.Path, settings)))
		}
	}
	if settings.Mise {
		cmd.Printf("    mise gh-only shim:    installed=%t\n", shims.IsMiseGHInstalled())
	}
	if realGHPath, err := ghauth.ResolveGHPath(); err == nil {
		cmd.Printf("    real gh binary:       %s\n", sanitizeStatusValue(realGHPath))
	} else {
		cmd.Println("    real gh binary:       unavailable (runtime discovery failed)")
	}
	if !shims.NativeGHSupported() {
		cmd.Println("    native gh proxy:      unsupported on this platform")
	} else if shims.IsNativeGHInstalled() {
		cmd.Println("    native gh proxy:      installed")
	} else {
		cmd.Println("    native gh proxy:      missing, broken, or unmanaged")
	}
	if settings.ExternalShell {
		cmd.Println("    shim PATH:            external, not verified")
	} else if shims.CheckPathConfigured() {
		cmd.Println("    shim PATH configured: yes")
	} else {
		cmd.Println("    shim PATH configured: no")
	}
	helperPath, helperErr := gitcred.HelperPath()
	helperInfo, statErr := os.Stat(helperPath)
	if helperErr == nil && statErr == nil && config.IsExecutable(helperInfo) {
		cmd.Println("    git helper installed: yes")
	} else {
		cmd.Println("    git helper installed: no (required; missing or not executable)")
	}
	helperHost := "github.com"
	if pin != nil {
		helperHost = pin.Host
	}
	// Git builds the helper list from every config level, so a repository can
	// clear the global setting; report what applies in this directory.
	helperActive, resetBy, activeErr := gitcred.ManagedHelperActive(cwd, helperHost)
	switch {
	case activeErr != nil:
		cmd.Printf("    git helper active:    unknown (%s)\n", sanitizeStatusValue(activeErr.Error()))
	case helperActive:
		cmd.Println("    git helper active:    yes")
	case resetBy != "":
		cmd.Printf("    git helper active:    no; %s clears the helper list for %s here\n", sanitizeStatusValue(resetBy), sanitizeStatusValue(helperHost))
		cmd.Println("                          remove that entry, or add the managed helper after it, to route Git HTTPS")
	default:
		cmd.Println("    git helper active:    no (required)")
	}

	// Diagnostics
	cmd.Println()
	if pin != nil && pin.Owner != "" {
		cmd.Println("  Owner pin: selected per command from the accessed repository's owner; it outranks directory pins.")
	} else if pin != nil {
		cmd.Println("  Native routing selects this pin from the registry per command.")
		if ghTokenPresent || githubTokenPresent || enterpriseTokenPresent {
			cmd.Println("  Inherited GitHub token variables remain in the environment; local Git does not sanitize them.")
		}
	} else if diagnostics.SelectionSource == "default" {
		cmd.Println("  Default account selected for authentication; Git identity is not changed.")
	} else if ghTokenPresent || githubTokenPresent || enterpriseTokenPresent {
		cmd.Println("  NOTE: Token variables are set but this directory has no pin.")
		cmd.Println("        They come from another source, such as an export in your shell.")
	} else {
		cmd.Println("  No pin selected. Default gh behavior; GitHub identity is not verified.")
	}
	if pin != nil && helperActive {
		cmd.Println("  Plain git HTTPS uses the built-in gh-autoaccount credential helper.")
	}

	return nil
}

func presentLabel(present bool) string {
	if present {
		return "(present)"
	}
	return "(not set)"
}

func sanitizeStatusValue(value string) string {
	var result strings.Builder
	result.Grow(len(value))
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		result.WriteRune(r)
	}
	return result.String()
}
