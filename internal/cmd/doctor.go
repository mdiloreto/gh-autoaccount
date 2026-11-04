package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/legacy"
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
		Long:  "Validate the gh proxy, PATH, Git credential helper, optional Git identity, pin schema, and files left by retired modes.",
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
		setupCmd.SetErr(cmd.ErrOrStderr())
		return runSetup(setupCmd, args)
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("cannot load setup settings: %w", err)
	}
	issues := 0
	fmt.Println("gh-autoaccount doctor")
	fmt.Println("=====================")

	shimPath, shimPathErr := shims.GHPath()
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

	if settings.Mise {
		if shims.IsMiseGHInstalled() {
			fmt.Println("OK   mise gh-only shim installed")
		} else {
			fmt.Println("WARN mise gh-only shim missing, broken, or unmanaged")
			issues++
		}
		if dir, err := shims.MiseGHDir(); err == nil {
			fmt.Printf("     path: %s\n", dir)
		}
	}

	pathVerified := false
	if shims.NativeGHSupported() && shims.IsNativeGHInstalled() {
		var pathIssues int
		pathVerified, pathIssues = checkGHOnPath(settings)
		issues += pathIssues
	}
	if !pathVerified {
		if settings.ExternalShell {
			fmt.Println("INFO external PATH integration not verified")
		} else if shims.CheckPathConfigured() {
			fmt.Println("OK   shim PATH block configured")
		} else {
			fmt.Println("INFO shim PATH block not configured")
		}
	}

	helperPath, helperErr := gitcred.HelperPath()
	helperInfo, statErr := os.Stat(helperPath)
	helperUsable := helperErr == nil && statErr == nil && helperInfo.Mode().IsRegular() && helperInfo.Mode().Perm()&0111 != 0
	if helperUsable {
		fmt.Println("OK   git credential helper installed")
		fmt.Printf("     path: %s\n", helperPath)
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
		if realGH, realErr := gitcred.RealGH(); realErr != nil {
			fmt.Printf("WARN cannot inspect git credential helper gh: %v\n", realErr)
			issues++
		} else if realGH != "" { // older helpers pin no gh and resolve it at runtime
			if info, statErr := os.Stat(realGH); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				fmt.Printf("WARN git credential helper gh is missing or not executable: %s\n", realGH)
				fmt.Println("     repair with: gh autoaccount setup --migrate")
				issues++
			} else {
				fmt.Printf("OK   git credential helper gh: %s\n", realGH)
			}
		}
	} else {
		fmt.Println("WARN required git credential helper missing or not executable")
		issues++
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
	if settings.GitIdentity {
		if err := gitidentity.CheckOwnerIdentitySupport(registry); err != nil {
			fmt.Printf("WARN %v\n", err)
			issues++
		}
	}
	{
		hosts, err := gitcred.Hosts(registry)
		if err != nil {
			return fmt.Errorf("cannot inspect pin hosts: %w", err)
		}
		pathHosts, err := gitcred.OwnerHosts(registry)
		if err != nil {
			return fmt.Errorf("cannot inspect owner pin hosts: %w", err)
		}
		needsPath := map[string]bool{}
		for _, host := range pathHosts {
			needsPath[host] = true
		}
		configured := true
		for _, host := range hosts {
			if !gitcred.CheckConfiguredHost(host) {
				configured = false
				fmt.Printf("WARN git credential helper not configured for %s\n", host)
				issues++
			} else if needsPath[host] && !gitcred.CheckUseHTTPPathHost(host) {
				configured = false
				fmt.Printf("WARN git credential.https://%s.useHttpPath is not true (owner pins need request paths)\n", host)
				issues++
			}
		}
		if configured {
			fmt.Println("OK   git credential helper configured")
		} else if !gitcred.CheckConfigured() {
			fmt.Println("WARN required git credential helper not configured")
		}
	}

	var leftovers []string
	for _, pin := range registry.Pins {
		if pin.Mode != config.ModeWrapper {
			target := pin.Dir
			if pin.IsOwner() {
				target = "owner " + pin.Owner
			}
			leftovers = append(leftovers, fmt.Sprintf("pin for %s is not migrated (mode %q)", target, pin.Mode))
		}
		if !pin.IsOwner() && legacy.HasManagedEnvrc(pin.Dir) {
			leftovers = append(leftovers, filepath.Join(pin.Dir, ".envrc")+" has a retired managed block")
		}
	}
	leftovers = append(leftovers, legacy.LeftoverFiles()...)
	if rcFiles, err := legacy.RCFiles(); err == nil {
		for _, rc := range rcFiles {
			if legacy.HasHookSource(rc) {
				leftovers = append(leftovers, rc+" sources the retired hook")
			}
		}
	}
	if len(leftovers) == 0 {
		fmt.Println("OK   no files left from the retired direnv modes")
	} else {
		fmt.Println("WARN files left from the retired direnv modes:")
		for _, item := range leftovers {
			fmt.Printf("     %s\n", item)
		}
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
