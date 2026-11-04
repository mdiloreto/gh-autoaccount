package cmd

import (
	"encoding/json"
	"os"
	osexec "os/exec"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
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
		Long: `Display the selected directory's pinned account, token mode,
environment markers, credential availability, and actual Git identities.
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
		cmd.Printf("  Token mode:       %s\n", sanitizeStatusValue(string(pin.Mode)))
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
	hostTokenPresent := ghTokenPresent || githubTokenPresent
	if pin != nil && pin.Host != "github.com" && !strings.HasSuffix(pin.Host, ".ghe.com") {
		hostTokenPresent = enterpriseTokenPresent
	}
	autoprofileUser := diagnostics.Environment.ProfileUser
	autoprofileMode := diagnostics.Environment.ProfileMode

	cmd.Println("  Environment:")
	if diagnostics.Environment.ProfileHost != "" {
		cmd.Printf("    GH_AUTOPROFILE_HOST:  %s\n", sanitizeStatusValue(diagnostics.Environment.ProfileHost))
	}
	if autoprofileUser != "" {
		cmd.Printf("    GH_AUTOPROFILE_USER:  %s\n", sanitizeStatusValue(autoprofileUser))
	} else {
		cmd.Println("    GH_AUTOPROFILE_USER:  (not set)")
	}
	if autoprofileMode != "" {
		cmd.Printf("    GH_AUTOPROFILE_MODE:  %s\n", sanitizeStatusValue(autoprofileMode))
	} else {
		cmd.Println("    GH_AUTOPROFILE_MODE:  (not set)")
	}
	if ghTokenPresent {
		cmd.Println("    GH_TOKEN:             (present)")
	} else {
		cmd.Println("    GH_TOKEN:             (not set)")
	}
	if githubTokenPresent {
		cmd.Println("    GITHUB_TOKEN:         (present)")
	} else {
		cmd.Println("    GITHUB_TOKEN:         (not set)")
	}
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
	if settings.Native {
		cmd.Println("    routing mode:         native (registry-based)")
	} else {
		cmd.Println("    routing mode:         legacy")
		cmd.Println("    WARN: " + legacyDeprecation)
	}
	if settings.ExternalShell {
		cmd.Println("    shell integration:    external, not verified (RC management disabled)")
	}
	if settings.GitIdentity {
		cmd.Printf("    Git identity include registered: %t\n", gitidentity.CheckRegistered())
		cmd.Println("    Git identity follows gitdir; local config and environment can override.")
	} else {
		cmd.Println("    managed Git identity: disabled (setup --git-identity to enable)")
	}
	if ghPath, err := osexec.LookPath("gh"); err == nil {
		cmd.Printf("    gh on PATH:           %s\n", sanitizeStatusValue(ghPath))
	}
	if realGHPath, err := ghauth.ResolveGHPath(); err == nil {
		cmd.Printf("    real gh binary:       %s\n", sanitizeStatusValue(realGHPath))
	} else if settings.Native {
		cmd.Println("    real gh binary:       unavailable (runtime discovery failed)")
	}
	if settings.Native {
		if !shims.NativeGHSupported() {
			cmd.Println("    native gh proxy:      unsupported on this platform")
		} else if shims.IsNativeGHInstalled() {
			cmd.Println("    native gh proxy:      installed")
		} else {
			cmd.Println("    native gh proxy:      missing, broken, or unmanaged")
		}
	} else if shims.IsGHInstalled() {
		cmd.Println("    gh shim installed:    yes")
	} else {
		cmd.Println("    gh shim installed:    no")
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
	if helperErr == nil && statErr == nil && helperInfo.Mode().IsRegular() && helperInfo.Mode().Perm()&0111 != 0 {
		cmd.Println("    git helper installed: yes")
	} else if settings.Native {
		cmd.Println("    git helper installed: no (required; missing or not executable)")
	} else {
		cmd.Println("    git helper installed: no")
	}
	helperHost := "github.com"
	if pin != nil {
		helperHost = pin.Host
	}
	helperConfigured := gitcred.CheckConfiguredHost(helperHost)
	if helperConfigured {
		cmd.Println("    git helper active:    yes")
	} else if settings.Native {
		cmd.Println("    git helper active:    no (required)")
	} else {
		cmd.Println("    git helper active:    no")
	}

	// Diagnostics
	cmd.Println()
	if pin != nil && pin.Owner != "" {
		cmd.Println("  Owner pin: selected per command from the accessed repository's owner; it outranks directory pins.")
		if helperConfigured {
			cmd.Println("  Plain git HTTPS uses the built-in gh-autoaccount credential helper.")
		}
	} else if pin != nil {
		mode := pin.Mode
		if mode == config.ModeWrapper {
			if settings.Native {
				cmd.Println("  Native routing selects this pin from the registry without direnv markers.")
				if ghTokenPresent || githubTokenPresent || enterpriseTokenPresent {
					cmd.Println("  Inherited GitHub token variables remain in the environment; local Git does not sanitize them.")
				}
			} else {
				// Wrapper mode: expect GH_AUTOPROFILE_USER set and tokens cleared from
				// the directory environment.
				if autoprofileUser == "" {
					cmd.Println("  WARNING: Directory is pinned (wrapper mode) but GH_AUTOPROFILE_USER is not set.")
					cmd.Println("           Is direnv loaded? Try: cd . (to re-trigger direnv)")
					printDirenvDiagnostics(cmd)
				} else if autoprofileMode != "" && autoprofileMode != string(config.ModeWrapper) {
					cmd.Printf("  WARNING: Directory is pinned (wrapper mode) but GH_AUTOPROFILE_MODE=%s.\n", sanitizeStatusValue(autoprofileMode))
					cmd.Println("           Run: gh autoaccount setup --migrate && direnv reload")
				} else if ghTokenPresent || githubTokenPresent || enterpriseTokenPresent {
					if os.Getenv("GH_AUTOPROFILE_REAL_GH") != "" {
						cmd.Println("  Profile markers indicate wrapper mode via command-scoped shim injection.")
					} else {
						cmd.Println("  WARNING: Wrapper mode is active but ambient GitHub token variables are still set.")
						cmd.Println("           This usually means the installed shell library is outdated or the token was exported after direnv loaded.")
						cmd.Println("           Run: gh autoaccount setup --migrate && direnv reload")
					}
				} else if settings.ExternalShell {
					cmd.Println("  Profile markers are set; external shell wrapper integration is not verified.")
				} else {
					cmd.Println("  Profile markers are set (wrapper mode). Tokens are resolved per command.")
				}
			}
		} else {
			// Export mode: expect GH_TOKEN set
			if autoprofileMode != "" && autoprofileMode != string(config.ModeExport) {
				cmd.Printf("  WARNING: Directory is pinned (export mode) but GH_AUTOPROFILE_MODE=%s.\n", sanitizeStatusValue(autoprofileMode))
				cmd.Println("           Run: gh autoaccount setup --migrate && direnv reload")
			} else if !hostTokenPresent {
				cmd.Println("  WARNING: Directory is pinned (export mode) but its host-class token variables are not set.")
				cmd.Println("           Is direnv loaded? Try: cd . (to re-trigger direnv)")
				printDirenvDiagnostics(cmd)
			} else {
				cmd.Println("  Export mode: a GitHub token is present in the environment.")
			}
		}
		if mode == config.ModeWrapper && !settings.Native && shims.IsGHInstalled() && !settings.ExternalShell {
			cmd.Println("  Plain gh can also use the PATH shim in non-interactive shells.")
		}
		if helperConfigured {
			cmd.Println("  Plain git HTTPS uses the built-in gh-autoaccount credential helper.")
		}
	} else if diagnostics.SelectionSource == "default" {
		cmd.Println("  Default account selected for authentication; Git identity is not changed.")
	} else if ghTokenPresent || githubTokenPresent || enterpriseTokenPresent || autoprofileUser != "" {
		cmd.Println("  NOTE: Token/profile vars are set but this directory has no pin.")
		cmd.Println("        They may come from another source (parent .envrc, export, etc.)")
	} else {
		cmd.Println("  No pin selected. Default gh behavior; GitHub identity is not verified.")
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

func printDirenvDiagnostics(cmd *cobra.Command) {
	if settings, err := config.LoadSettings(); err == nil && settings.ExternalShell {
		cmd.Println("           External shell integration is not verified; check your shell manager's direnv and wrapper hooks.")
		return
	}
	if !direnvlib.IsInstalled() {
		cmd.Println("           direnv is not installed!")
	} else if !direnvlib.IsShellLibInstalled() {
		if libPath, err := direnvlib.ShellLibPath(); err == nil {
			cmd.Printf("           Shell library not installed at %s. Run: gh autoaccount setup\n", sanitizeStatusValue(libPath))
		} else {
			cmd.Println("           Shell library not installed. Run: gh autoaccount setup")
		}
	} else if !direnvlib.CheckDirenvHook() {
		cmd.Println("           direnv shell hook not detected in your shell config.")
	}
}
