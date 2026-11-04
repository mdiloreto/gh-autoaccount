package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
	"github.com/spf13/cobra"
)

// configPermIssues returns human-readable warnings for existing config paths
// whose permissions are looser than the documented hardening targets (config
// dir 0700, pins.yml 0600). Paths that do not exist are skipped.
func configPermIssues() []string {
	var warnings []string
	if dir, err := config.ConfigDir(); err == nil {
		if fi, statErr := os.Stat(dir); statErr == nil && fi.Mode().Perm() != 0700 {
			warnings = append(warnings, fmt.Sprintf("config directory %s is %04o, want 0700", dir, fi.Mode().Perm()))
		}
	}
	if path, err := config.PinsFilePath(); err == nil {
		if fi, statErr := os.Stat(path); statErr == nil && fi.Mode().Perm() != 0600 {
			warnings = append(warnings, fmt.Sprintf("pins file %s is %04o, want 0600", path, fi.Mode().Perm()))
		}
	}
	return warnings
}

// NewDoctorCmd creates the `doctor` subcommand.
func NewDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check setup and migration health",
		Long:  "Validate shell integration, optional Git identity, pin schema, and managed .envrc permissions.",
		RunE:  runDoctor,
	}
	cmd.Flags().Bool("fix", false, "Run setup migration automatically")
	return cmd
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fix, err := cmd.Flags().GetBool("fix")
	if err != nil {
		return err
	}
	if fix {
		setupCmd := NewSetupCmd()
		if err := setupCmd.Flags().Set("migrate", "true"); err != nil {
			return err
		}
		return runSetup(setupCmd, args)
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("cannot load setup settings: %w", err)
	}
	issues := 0
	fmt.Println("gh-autoaccount doctor")
	fmt.Println("=====================")
	if settings.Native {
		fmt.Println("INFO routing mode: native (registry-based gh and Git HTTPS)")
		fmt.Println("INFO direnv is optional for native routing; export workflows require it")
	} else {
		fmt.Println("INFO routing mode: legacy (shell wrappers and optional shim)")
	}

	libPath, libPathErr := direnvlib.ShellLibPath()
	if direnvlib.IsShellLibInstalled() {
		fmt.Println("OK   direnv library installed")
		if libPathErr == nil {
			fmt.Printf("     path: %s\n", libPath)
		}
	} else {
		if settings.Native {
			fmt.Println("WARN direnv library missing (export workflows unavailable; native routing unaffected)")
		} else {
			fmt.Println("WARN direnv library missing")
		}
		if libPathErr == nil {
			fmt.Printf("     expected path: %s\n", libPath)
		}
		issues++
	}

	if settings.ExternalShell {
		fmt.Println("INFO external shell integration not verified (RC management disabled)")
	} else if direnvlib.CheckShellHookInstalled() {
		fmt.Println("OK   shell hook source configured")
	} else if settings.Native {
		fmt.Println("INFO shell hook source not detected (optional for native routing; needed for legacy integration)")
	} else {
		fmt.Println("WARN shell hook source not detected")
		issues++
	}

	shimPath, shimPathErr := shims.GHPath()
	if settings.Native {
		if !shims.NativeGHSupported() {
			fmt.Println("WARN native gh routing is unsupported on this platform")
			issues++
		}
		if shims.IsNativeGHInstalled() {
			fmt.Println("OK   native gh proxy installed")
		} else {
			fmt.Println("WARN required native gh proxy missing, broken, or unmanaged")
			issues++
		}
		if shimPathErr == nil {
			fmt.Printf("     path: %s\n", shimPath)
		}
		if realGH, resolveErr := ghauth.ResolveGHPath(); resolveErr != nil {
			fmt.Printf("WARN cannot discover real gh at runtime: %v\n", resolveErr)
			issues++
		} else {
			fmt.Printf("OK   runtime real gh: %s\n", realGH)
		}
	} else if shims.IsGHInstalled() {
		fmt.Println("OK   gh shim installed")
		if shimPathErr == nil {
			fmt.Printf("     path: %s\n", shimPath)
		}
		realGH, resolveErr := ghauth.ResolveGHPath()
		if resolveErr != nil {
			fmt.Printf("WARN cannot verify gh shim target: %v\n", resolveErr)
			issues++
		} else if current, targetErr := shims.TargetsGH(realGH); targetErr != nil {
			fmt.Printf("WARN cannot inspect gh shim target: %v\n", targetErr)
			issues++
		} else if current {
			fmt.Println("OK   gh shim target matches current gh")
		} else {
			fmt.Println("WARN gh shim target is stale or uses the legacy format")
			fmt.Printf("     current gh: %s\n", realGH)
			fmt.Println("     refresh with: gh autoaccount setup --migrate")
			issues++
		}
	} else {
		fmt.Println("INFO gh shim not installed (optional)")
		if shimPathErr == nil {
			fmt.Printf("     expected path: %s\n", shimPath)
		}
	}

	if settings.ExternalShell {
		fmt.Println("INFO external PATH integration not verified")
	} else if shims.CheckPathConfigured() {
		fmt.Println("OK   shim PATH block configured")
	} else {
		fmt.Println("INFO shim PATH block not configured")
	}

	helperPath, helperErr := gitcred.HelperPath()
	helperInfo, statErr := os.Stat(helperPath)
	helperUsable := helperErr == nil && statErr == nil && helperInfo.Mode().IsRegular() && helperInfo.Mode().Perm()&0111 != 0
	if helperUsable {
		fmt.Println("OK   git credential helper installed")
		fmt.Printf("     path: %s\n", helperPath)
		if settings.Native {
			if target, targetErr := gitcred.Target(); targetErr != nil {
				fmt.Printf("WARN cannot inspect git credential helper target: %v\n", targetErr)
				issues++
			} else if info, statErr := os.Stat(target); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				fmt.Printf("WARN git credential helper target is missing or not executable: %s\n", target)
				fmt.Println("     repair with: gh autoaccount setup --migrate")
				issues++
			} else {
				fmt.Printf("OK   git credential helper target: %s\n", target)
			}
		}
	} else if settings.Native {
		fmt.Println("WARN required git credential helper missing or not executable")
		issues++
	} else {
		fmt.Println("INFO git credential helper not installed (optional)")
	}

	if !settings.GitIdentity {
		fmt.Println("INFO managed Git identity disabled (optional; setup --git-identity)")
	} else if gitidentity.CheckRegistered() {
		fmt.Println("OK   managed Git identity include registered (local config and environment can override)")
	} else {
		fmt.Println("WARN managed Git identity include missing or not uniquely registered")
		issues++
	}

	users, err := ghauth.ListUsers()
	if err != nil {
		fmt.Printf("WARN cannot inspect gh accounts: %v\n", err)
		issues++
	} else {
		invalidCount := 0
		for _, user := range users {
			if !user.Valid {
				invalidCount++
			}
		}
		if invalidCount == 0 {
			fmt.Println("OK   gh account entries are valid")
		} else {
			fmt.Printf("WARN %d gh account entrie(s) are invalid\n", invalidCount)
			issues++
		}

	}

	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pins: %w", err)
	}
	if gitcred.CheckConfigured() || settings.Native {
		hosts, err := gitcred.Hosts(registry)
		if err != nil {
			return fmt.Errorf("cannot inspect pin hosts: %w", err)
		}
		configured := true
		for _, host := range hosts {
			if !gitcred.CheckConfiguredHost(host) {
				configured = false
				fmt.Printf("WARN git credential helper not configured for %s\n", host)
				issues++
			}
		}
		if configured {
			fmt.Println("OK   git credential helper configured")
		} else if settings.Native && !gitcred.CheckConfigured() {
			fmt.Println("WARN required git credential helper not configured")
		}
	} else {
		fmt.Println("INFO git credential helper not configured (optional; setup --credential-helper)")
	}

	missingModes := 0
	envrcPermIssues := 0
	for _, pin := range registry.Pins {
		if pin.Mode == "" {
			missingModes++
		}
		envrcPath := filepath.Join(pin.Dir, ".envrc")
		if fi, err := os.Stat(envrcPath); err == nil {
			if fi.Mode().Perm() != 0600 {
				envrcPermIssues++
			}
		}
	}

	if missingModes == 0 {
		fmt.Println("OK   pin modes normalized")
	} else {
		fmt.Printf("WARN %d pin(s) missing mode (will default to wrapper)\n", missingModes)
		issues++
	}

	if envrcPermIssues == 0 {
		fmt.Println("OK   managed .envrc permissions are 0600")
	} else {
		fmt.Printf("WARN %d managed .envrc file(s) not 0600\n", envrcPermIssues)
		issues++
	}

	permIssues := configPermIssues()
	if len(permIssues) == 0 {
		fmt.Println("OK   config directory and pins file permissions are hardened")
	} else {
		for _, msg := range permIssues {
			fmt.Printf("WARN %s\n", msg)
			issues++
		}
		fmt.Println("     repair with: gh autoaccount setup --migrate")
	}

	if issues == 0 {
		if settings.ExternalShell {
			fmt.Println("\nManaged files checked; external shell integration remains unverified.")
		} else {
			fmt.Println("\nDoctor check passed.")
		}
		return nil
	}

	fmt.Println("\nRun `gh autoaccount setup --migrate` to fix detected issues.")
	return nil
}
