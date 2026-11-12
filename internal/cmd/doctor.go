package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/legacy"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
	"github.com/spf13/cobra"
)

func doctorPrintf(format string, args ...any) {
	output := sanitizeStatusValue(fmt.Sprintf(format, args...))
	// Preserve only the trusted format's trailing newline.
	if strings.HasSuffix(format, "\n") {
		output += "\n"
	}
	fmt.Print(output)
}

// configPermIssues returns human-readable warnings for existing config paths
// whose permissions are looser than the documented hardening targets (config
// dir 0700, pins.yml 0600). Paths that do not exist are skipped.
func configPermIssues() []string {
	// Windows has no POSIX mode bits; the profile directory's ACL keeps it private.
	if runtime.GOOS == "windows" {
		return nil
	}
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
	// Problems that setup --migrate cannot fix are counted separately, so the
	// closing hint never sends the user to a command that changes nothing.
	issues, manualIssues := 0, 0
	fmt.Println("gh-autoaccount doctor")
	fmt.Println("=====================")

	shimPath, shimPathErr := shims.GHPath()
	if !shims.NativeGHSupported() {
		fmt.Println("WARN native gh routing is unsupported on this platform")
		issues++
		manualIssues++
	}
	if shims.IsNativeGHInstalled() {
		fmt.Println("OK   native gh proxy installed")
	} else {
		fmt.Println("WARN required native gh proxy missing, broken, or unmanaged")
		issues++
	}
	if shimPathErr == nil {
		doctorPrintf("     path: %s\n", shimPath)
	}
	if realGH, resolveErr := ghauth.ResolveGHPath(); resolveErr != nil {
		doctorPrintf("WARN cannot discover real gh at runtime: %v\n", resolveErr)
		issues++
	} else {
		doctorPrintf("OK   runtime real gh: %s\n", realGH)
	}

	if settings.Mise {
		if shims.IsMiseGHInstalled() {
			fmt.Println("OK   mise gh-only shim installed")
		} else {
			fmt.Println("WARN mise gh-only shim missing, broken, or unmanaged")
			issues++
		}
		if dir, err := shims.MiseGHDir(); err == nil {
			doctorPrintf("     path: %s\n", dir)
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
	helperUsable := helperErr == nil && statErr == nil && config.IsExecutable(helperInfo)
	if helperUsable {
		fmt.Println("OK   git credential helper installed")
		doctorPrintf("     path: %s\n", helperPath)
		if target, targetErr := gitcred.Target(); targetErr != nil {
			doctorPrintf("WARN cannot inspect git credential helper target: %v\n", targetErr)
			issues++
		} else if info, statErr := os.Stat(target); statErr != nil || !config.IsExecutable(info) {
			doctorPrintf("WARN git credential helper target is missing or not executable: %s\n", target)
			fmt.Println("     repair with: gh autoaccount setup --migrate")
			issues++
		} else {
			doctorPrintf("OK   git credential helper target: %s\n", target)
			if shims.NativeGHStale(target) {
				fmt.Println("WARN native gh proxy is a copy of an older extension binary (gh extension upgrade replaced it)")
				fmt.Println("     refresh with: gh autoaccount setup --migrate")
				issues++
			}
		}
		if realGH, realErr := gitcred.RealGH(); realErr != nil {
			doctorPrintf("WARN cannot inspect git credential helper gh: %v\n", realErr)
			issues++
		} else if realGH != "" { // older helpers pin no gh and resolve it at runtime
			if info, statErr := os.Stat(realGH); statErr != nil || !config.IsExecutable(info) {
				doctorPrintf("WARN git credential helper gh is missing or not executable: %s\n", realGH)
				fmt.Println("     repair with: gh autoaccount setup --migrate")
				issues++
			} else {
				doctorPrintf("OK   git credential helper gh: %s\n", realGH)
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
		doctorPrintf("WARN cannot inspect gh accounts: %v\n", err)
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
			doctorPrintf("WARN %d gh account entrie(s) are invalid\n", invalidCount)
			fmt.Println("     log in again with: gh auth login")
			issues++
			manualIssues++
		}

	}

	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pins: %w", err)
	}
	if settings.GitIdentity {
		if err := gitidentity.CheckOwnerIdentitySupport(registry); err != nil {
			doctorPrintf("WARN %v\n", err)
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
				doctorPrintf("WARN git credential helper not configured for %s\n", host)
				issues++
			} else if needsPath[host] && !gitcred.CheckUseHTTPPathHost(host) {
				configured = false
				doctorPrintf("WARN git credential.https://%s.useHttpPath is not true (owner pins need request paths)\n", host)
				issues++
			}
		}
		if configured {
			fmt.Println("OK   git credential helper configured")
		} else if !gitcred.CheckConfigured() {
			fmt.Println("WARN required git credential helper not configured")
		}
	}

	// A repository can clear the global helper list; check this directory for
	// every pinned host whose global helper is in place (others are reported above).
	if cwd, err := os.Getwd(); err == nil {
		hosts, _ := gitcred.Hosts(registry)
		for _, host := range hosts {
			if !gitcred.CheckConfiguredHost(host) {
				continue
			}
			state, err := gitcred.ManagedHelper(cwd, host)
			switch {
			case err != nil:
				doctorPrintf("WARN cannot read the Git credential helper list for %s in %s: %v\n", host, cwd, err)
				issues++
				manualIssues++
			case !state.Active && state.ResetBy != "":
				doctorPrintf("WARN in %s, %s clears the Git credential helper list for %s, so Git HTTPS there skips gh-autoaccount\n", cwd, state.ResetBy, host)
				fmt.Println("     remove that entry, or add the managed helper after it")
				issues++
				manualIssues++
			case state.RunsAfter != "":
				doctorPrintf("INFO in %s, Git asks %s for %s credentials before gh-autoaccount\n", cwd, state.RunsAfter, host)
			}
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
	// setup removes managed hook blocks itself, except in external-shell mode,
	// where the RC files belong to the user.
	hookRefs, scanComplete := legacy.HookReferences()
	if rcFiles, err := legacy.RCFiles(); err == nil {
		for _, rc := range rcFiles {
			if !legacy.HasHookSource(rc) {
				continue
			}
			if settings.ExternalShell {
				hookRefs = append(hookRefs, rc)
			} else {
				leftovers = append(leftovers, rc+" has the retired hook source block")
			}
		}
	}
	hookPath, _ := legacy.ShellHookPath()
	_, hookErr := os.Lstat(hookPath)
	hookExists := hookErr == nil
	// setup keeps the hook while a shell may still source it.
	hookKept := hookExists && (settings.ExternalShell || len(hookRefs) > 0 || !scanComplete)
	for _, path := range legacy.LeftoverFiles() {
		if path != hookPath || !hookKept {
			leftovers = append(leftovers, path)
		}
	}
	if len(leftovers) > 0 {
		fmt.Println("WARN files left from the retired direnv modes:")
		for _, item := range leftovers {
			doctorPrintf("     %s\n", item)
		}
		issues++
	}
	switch {
	case !hookExists && len(hookRefs) > 0:
		doctorPrintf("WARN your shell config still sources the retired hook %s, which no longer exists\n", hookPath)
		for _, rc := range hookRefs {
			doctorPrintf("     referenced from: %s\n", rc)
		}
		fmt.Println("     remove the lines that source it")
		issues++
		manualIssues++
	case hookKept:
		doctorPrintf("WARN the retired hook %s is kept because your shell config may still source it\n", hookPath)
		for _, rc := range hookRefs {
			doctorPrintf("     referenced from: %s\n", rc)
		}
		if !scanComplete {
			fmt.Println("     some shell config files could not be fully checked")
		}
		fmt.Println("     remove the lines that source it, then delete the file; setup --migrate leaves it in place")
		issues++
		manualIssues++
	}
	if len(leftovers) == 0 && !hookKept && len(hookRefs) == 0 {
		fmt.Println("OK   no files left from the retired direnv modes")
	}

	permIssues := configPermIssues()
	if len(permIssues) == 0 {
		fmt.Println("OK   config directory and pins file permissions are hardened")
	} else {
		for _, msg := range permIssues {
			doctorPrintf("WARN %s\n", msg)
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

	fmt.Println()
	if issues > manualIssues {
		fmt.Println("Run `gh autoaccount setup --migrate` to fix detected issues.")
	}
	if manualIssues > 0 {
		fmt.Println("Some issues need the manual steps shown with them; setup --migrate does not change those.")
	}
	return nil
}
