package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
	"github.com/spf13/cobra"
)

// NewSetupCmd creates the `setup` subcommand.
func NewSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install shell integration and optional non-interactive tooling",
		Long: `Checks that gh CLI (>= 2.40.0) is installed, then installs:

  1. The direnv shell library (use_gh_autoprofile / use_gh_autoprofile_export)
  2. The shell hook (native wrapper cleanup, or deprecated legacy wrappers)
  3. For native routing, a PATH-level gh proxy and GitHub HTTPS credential helper
  4. Optionally, Git-native path-scoped identity (--git-identity)
  5. Optionally, mise compatibility (--mise) for native routing

Use --no-shell-rc for externally managed shell integration. This preference is
persisted: future setup, --migrate, and doctor --fix leave RC and PATH untouched.
Use --no-shell-rc=false to return to automatic RC integration.

Native routing is the default on Linux and macOS for a fresh install: registry-
based gh routing and the required Git HTTPS credential helper, without shell
wrappers, so non-interactive shells (agents, editor tasks, cron) are covered.
Direnv is optional for native routing but remains required for export workflows.
An existing legacy installation keeps legacy routing, which is deprecated, until
you run setup --native. Use --native=false to select legacy gh and hook routing
explicitly; the configured Git helper and identity settings remain in place.
Git identity remains separately opt-in. Legacy setup requires direnv.

Run this once after installing gh-autoaccount.

Use --migrate after upgrading to refresh generated files,
repair permissions, and update existing pins to the latest defaults.`,
		RunE: runSetup,
	}
	cmd.Flags().Bool("migrate", false, "Migrate existing pins and rewrite managed .envrc files")
	cmd.Flags().Bool("gh-shim", false, "Install PATH-level gh shim for non-interactive auth")
	cmd.Flags().Bool("credential-helper", false, "Configure git credential helper for non-interactive auth (systemd, cron, scripts)")
	cmd.Flags().Bool("no-shell-rc", false, "Persist external shell integration; do not edit shell RC or PATH")
	cmd.Flags().Bool("git-identity", false, "Enable managed Git includeIf identity from pins (persists across migration)")
	cmd.Flags().Bool("native", false, "Use native gh routing and Git helper (default for a fresh install); false selects deprecated legacy routing and retains the helper")
	cmd.Flags().Bool("mise", false, "Keep plain gh on the managed shim when mise manages gh (native routing; persisted); false undoes it")
	cmd.Flags().Bool("alias", false, "Optionally configure the non-clobbering gh aa alias")
	return cmd
}

func runSetup(cmd *cobra.Command, args []string) (retErr error) {
	unlock, err := config.LockMutations()
	if err != nil {
		return err
	}
	defer unlock()
	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("cannot load setup settings: %w", err)
	}
	if cmd.Flags().Changed("no-shell-rc") {
		settings.ExternalShell, _ = cmd.Flags().GetBool("no-shell-rc")
	}
	previousNative := settings.Native
	if cmd.Flags().Changed("native") {
		settings.Native, _ = cmd.Flags().GetBool("native")
	} else if !settings.Native && nativeGHSupported() && freshInstall() {
		settings.Native = true
	}
	if notice := legacyNotice(); !settings.Native && notice != "" {
		fmt.Fprintln(cmd.ErrOrStderr(), "gh-autoaccount: "+notice)
	}
	if settings.Native && !shims.NativeGHSupported() {
		return fmt.Errorf("native gh routing is unsupported on this platform")
	}
	miseChanged := cmd.Flags().Changed("mise")
	if miseChanged {
		settings.Mise, _ = cmd.Flags().GetBool("mise")
	}
	if settings.Mise {
		if !settings.Native {
			if miseChanged {
				return fmt.Errorf("--mise requires native routing; run: gh autoaccount setup --native --mise")
			}
			return fmt.Errorf("mise integration requires native routing; run `gh autoaccount setup --mise=false` before leaving native routing")
		}
		if miseChanged {
			if _, err := miseExecutable(); err != nil {
				return fmt.Errorf("--mise requires the mise executable on PATH: %w", err)
			}
		}
		if err := shims.CheckMiseGHReplaceable(); err != nil {
			return err
		}
	}
	gitIdentity, _ := cmd.Flags().GetBool("git-identity")
	if gitIdentity {
		settings.GitIdentity = true
	}
	migrate, err := cmd.Flags().GetBool("migrate")
	if err != nil {
		return err
	}
	migrationRequested := migrate || gitIdentity
	if settings.Native {
		registry, err := config.LoadPins()
		if err != nil {
			return fmt.Errorf("cannot load pin registry: %w", err)
		}
		for _, pin := range registry.Pins {
			if !pin.IsOwner() && pin.EffectiveMode() == config.ModeWrapper {
				if err := direnvlib.RequireManualEnvrcCleanup(pin.Dir); err != nil {
					return err
				}
			}
		}
	}
	if previousNative && !settings.Native && !migrationRequested {
		fmt.Println("Legacy wrapper pins require direnv; run setup --migrate and reload your shell to activate native-only pins.")
	}
	installShim, err := cmd.Flags().GetBool("gh-shim")
	if err != nil {
		return err
	}
	credHelper, err := cmd.Flags().GetBool("credential-helper")
	if err != nil {
		return err
	}
	if migrate {
		installShim = installShim || shims.IsGHInstalled()
		credHelper = credHelper || gitcred.CheckConfigured()
	}
	if settings.Native || cmd.Flags().Changed("native") {
		installShim = true
	}

	fmt.Println("gh-autoaccount setup")
	fmt.Println("====================")
	fmt.Println()

	allGood := true

	// 1. Check gh CLI
	fmt.Print("  Checking gh CLI............. ")
	ghVersion, err := ghauth.GetGHVersion()
	if err != nil {
		fmt.Println("MISSING")
		fmt.Println("    gh CLI is required. Install from: https://cli.github.com")
		return fmt.Errorf("gh CLI not found")
	}
	if !isVersionAtLeast(ghVersion, "2.40.0") {
		fmt.Printf("v%s (TOO OLD)\n", ghVersion)
		return fmt.Errorf("gh CLI v2.40.0+ required for multi-account support (found v%s)", ghVersion)
	}
	fmt.Printf("v%s\n", ghVersion)

	// 2. Check direnv
	fmt.Print("  Checking direnv............. ")
	if !direnvlib.IsInstalled() && (settings.Native || (previousNative && cmd.Flags().Changed("native") && !settings.Native)) {
		fmt.Println("NOT INSTALLED (optional for native routing; required for export workflows)")
		if !settings.Native {
			fmt.Println("    Legacy wrappers and export workflows require direnv to activate.")
			allGood = false
		}
	} else if !direnvlib.IsInstalled() {
		fmt.Println("MISSING")
		fmt.Println()
		fmt.Println("  direnv is required. Install it:")
		fmt.Println("    Arch:   sudo pacman -S direnv")
		fmt.Println("    Ubuntu: sudo apt install direnv")
		fmt.Println("    macOS:  brew install direnv")
		fmt.Println()
		fmt.Println("  Then add the shell hook:")
		fmt.Println("    bash:  echo 'eval \"$(direnv hook bash)\"' >> ~/.bashrc")
		fmt.Println("    zsh:   echo 'eval \"$(direnv hook zsh)\"' >> ~/.zshrc")
		fmt.Println("    fish:  echo 'direnv hook fish | source' >> ~/.config/fish/config.fish")
		return fmt.Errorf("direnv not found")
	} else {
		direnvVersion, _ := direnvlib.GetVersion()
		fmt.Printf("v%s\n", direnvVersion)
	}

	// 3. Check direnv shell hook
	fmt.Print("  Checking direnv hook........ ")
	if settings.ExternalShell {
		fmt.Println("EXTERNAL (integration not verified)")
	} else if direnvlib.CheckDirenvHook() {
		fmt.Println("OK")
	} else {
		fmt.Println("NOT DETECTED")
		fmt.Println("    Add the direnv hook to your shell config:")
		fmt.Println("      bash: eval \"$(direnv hook bash)\"")
		fmt.Println("      zsh:  eval \"$(direnv hook zsh)\"")
		fmt.Println("      fish: direnv hook fish | source")
		if !settings.Native {
			allGood = false
		}
	}

	// 4. Check logged-in accounts
	fmt.Print("  Checking gh accounts........ ")
	users, err := ghauth.ListUsers()
	if err != nil {
		fmt.Println("ERROR")
		fmt.Printf("    %v\n", err)
		allGood = false
	} else {
		fmt.Printf("%d account(s)\n", len(users))
		for _, u := range users {
			marker := "  "
			if u.Active {
				marker = "* "
			}
			fmt.Printf("    %s%s (%s)\n", marker, u.User, u.Host)
		}
		if len(users) < 2 {
			fmt.Println("    Tip: Log in to more accounts with: gh auth login")
		}
	}

	libraryGH, err := ghauth.ResolveGHPath()
	if err != nil {
		return fmt.Errorf("cannot resolve real gh binary for shell library: %w", err)
	}
	libraryBinary, err := resolveBinaryPath()
	if err != nil {
		return err
	}
	// Capture only the files and Git helper chain touched by a mode transition.
	committed := false
	transition := settings.Native || cmd.Flags().Changed("native")
	var shimSnapshot, recordSnapshot, helperSnapshot, settingsSnapshot, libSnapshot, hookSnapshot setupFileSnapshot
	var rollback []func() error
	var priorHelpers gitcred.HelperSnapshot
	var helperHosts, helperPathHosts []string
	var helperRealGH string
	configDir, err := config.ConfigDir()
	if err != nil {
		return err
	}
	libPath, err := direnvlib.ShellLibPath()
	if err != nil {
		return err
	}
	hookPath, err := direnvlib.ShellHookPath()
	if err != nil {
		return err
	}
	for _, file := range []struct {
		path string
		dest *setupFileSnapshot
	}{
		{filepath.Join(configDir, "settings.yml"), &settingsSnapshot},
		{libPath, &libSnapshot}, {hookPath, &hookSnapshot},
	} {
		*file.dest, err = snapshotSetupFile(file.path)
		if err != nil {
			return err
		}
	}
	defer func() {
		if retErr == nil || committed {
			return
		}
		for i := len(rollback) - 1; i >= 0; i-- {
			retErr = errors.Join(retErr, rollback[i]())
		}
	}()
	if settings.Native || credHelper {
		helperRealGH, err = ghauth.ResolveGHPath()
		if err != nil {
			return fmt.Errorf("cannot resolve real gh binary for Git helper: %w", err)
		}
		registry, err := config.LoadPins()
		if err != nil {
			return err
		}
		helperHosts, err = gitcred.Hosts(registry)
		if err != nil {
			return err
		}
		if helperPathHosts, err = gitcred.OwnerHosts(registry); err != nil {
			return err
		}
		priorHelpers, err = gitcred.SnapshotHosts(helperHosts)
		if err != nil {
			return fmt.Errorf("cannot inspect Git helper configuration: %w", err)
		}
		helperPath, err := gitcred.HelperPath()
		if err != nil {
			return err
		}
		helperSnapshot, err = snapshotSetupFile(helperPath)
		if err != nil {
			return err
		}
		defer func() {
			if retErr != nil && !committed {
				retErr = errors.Join(retErr, gitcred.RestoreHosts(priorHelpers), helperSnapshot.restore())
			}
		}()
	}
	if transition {
		if err := shims.CheckGHReplaceable(); err != nil {
			return err
		}
		shimPath, err := shims.GHPath()
		if err != nil {
			return err
		}
		files := []struct {
			path string
			dest *setupFileSnapshot
		}{
			{shimPath, &shimSnapshot},
			{filepath.Join(configDir, "native-gh-targets.json"), &recordSnapshot},
		}
		for _, file := range files {
			snapshot, err := snapshotSetupFile(file.path)
			if err != nil {
				return err
			}
			*file.dest = snapshot
		}
	}
	restoreProxy := func() error {
		if err := shims.CheckGHReplaceable(); err != nil {
			return fmt.Errorf("cannot safely restore gh proxy: %w", err)
		}
		return errors.Join(recordSnapshot.restore(), shimSnapshot.restore())
	}
	configureMise := func() error {
		fmt.Print("  Configuring mise............ ")
		warning, err := applyMise(settings.Mise, libraryBinary, func(undo func() error) { rollback = append(rollback, undo) })
		if err != nil {
			fmt.Println("FAILED")
			return err
		}
		fmt.Println("OK")
		if warning != "" {
			fmt.Println("    " + warning)
			allGood = false
		}
		return nil
	}
	// Disabling runs first: a legacy transition drops the ownership record that
	// proves the gh-only shim is ours, and removal only touches an owned shim.
	if miseChanged && !settings.Mise {
		if err := configureMise(); err != nil {
			return err
		}
	}
	// Native mode requires both the proxy and Git helper before it is persisted.
	if settings.Native {
		binPath, err := resolveBinaryPath()
		if err != nil {
			return err
		}
		rollback = append(rollback, restoreProxy)
		if _, err := shims.InstallNativeGH(binPath); err != nil {
			return fmt.Errorf("cannot install native gh proxy: %w", err)
		}
		if _, err := gitcred.Install(binPath, helperRealGH); err != nil {
			return fmt.Errorf("cannot install required Git credential helper: %w", err)
		}
		if err := gitcred.ConfigureHosts(helperHosts, helperPathHosts...); err != nil {
			return fmt.Errorf("cannot configure required Git credential helper: %w", err)
		}
		credHelper = true
	} else if cmd.Flags().Changed("native") {
		realGH, err := ghauth.ResolveGHPath()
		if err != nil {
			return fmt.Errorf("cannot resolve real gh binary: %w", err)
		}
		binPath, err := resolveBinaryPath()
		if err != nil {
			return err
		}
		rollback = append(rollback, restoreProxy)
		if _, err := shims.InstallGH(binPath, realGH); err != nil {
			return fmt.Errorf("cannot restore legacy gh shim: %w", err)
		}
	}

	if settings.Mise {
		if err := configureMise(); err != nil {
			return err
		}
	}

	// 5. Install direnv shell library
	if err := config.SaveSettings(settings); err != nil {
		return fmt.Errorf("cannot save setup settings: %w", err)
	}
	rollback = append(rollback, settingsSnapshot.restore)
	fmt.Println()
	fmt.Print("  Installing direnv lib....... ")
	if err := direnvlib.InstallShellLib(libraryBinary, libraryGH); err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("cannot install shell library: %w", err)
	}
	rollback = append(rollback, libSnapshot.restore)
	fmt.Println("OK")
	fmt.Printf("    Installed: %s\n", libPath)

	// 6. Install shell hook (wrapper mode support)
	fmt.Print("  Installing shell hook....... ")
	hookPath, err = direnvlib.InstallShellHook()
	if err != nil {
		fmt.Println("FAILED")
		return fmt.Errorf("cannot install shell hook: %w", err)
	}
	rollback = append(rollback, hookSnapshot.restore)
	fmt.Println("OK")
	fmt.Printf("    Installed: %s\n", hookPath)

	if installShim && !settings.Native && !cmd.Flags().Changed("native") {
		fmt.Print("  Installing gh shim.......... ")
		realGH, err := ghauth.ResolveGHPath()
		if err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("cannot resolve real gh binary: %w", err)
		}
		binPath, err := resolveBinaryPath()
		if err != nil {
			fmt.Println("FAILED")
			return err
		}
		shimPath, err := shims.InstallGH(binPath, realGH)
		if err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("cannot install gh shim: %w", err)
		}
		fmt.Println("OK")
		fmt.Printf("    Installed: %s\n", shimPath)
	}

	// 7. Inject hook source into shell RC file
	fmt.Print("  Configuring shell RC........ ")
	rcPath := ""
	if settings.ExternalShell {
		fmt.Println("EXTERNAL (RC and PATH unchanged; integration not verified)")
		fmt.Printf("    Source the installed hook through your shell manager: %s\n", hookPath)
		if installShim {
			shimDir, _ := shims.Dir()
			fmt.Printf("    Ensure your externally managed PATH includes: %s\n", shimDir)
		}
	} else {
		rcPath, err = detectShellRC()
		if err != nil {
			fmt.Println("SKIPPED")
			fmt.Printf("    %v\n", err)
			fmt.Printf("    Add manually to your shell RC:\n")
			fmt.Printf("      source \"%s\"\n", hookPath)
			allGood = false
		} else {
			rcSnapshot, snapshotErr := snapshotSetupRC(rcPath)
			if snapshotErr != nil {
				fmt.Println("SKIPPED")
				fmt.Printf("    Cannot snapshot shell RC: %v\n", snapshotErr)
				allGood = false
			} else {
				rollback = append(rollback, rcSnapshot.restore)
				if direnvlib.CheckShellHookInstalled() {
					fmt.Println("OK (already configured)")
				} else {
					if err := direnvlib.InjectHookSource(rcPath, hookPath); err != nil {
						fmt.Println("FAILED")
						fmt.Printf("    %v\n", err)
						fmt.Printf("    Add manually to %s:\n", rcPath)
						fmt.Printf("      source \"%s\"\n", hookPath)
						allGood = false
					} else {
						fmt.Println("OK")
						fmt.Printf("    Added to: %s\n", rcPath)
					}
				}
				// A user who already puts the shim first without our block manages PATH.
				if installShim && (shims.CheckPathBlockInstalled() || !shimDirWinsForGH()) {
					shimDir, err := shims.Dir()
					if err != nil {
						fmt.Printf("    Cannot resolve shim dir: %v\n", err)
						allGood = false
					} else {
						for _, target := range []string{rcPath, loginProfile(rcPath)} {
							if target == "" || shims.HasPathBlock(target) {
								continue
							}
							if target != rcPath {
								snapshot, snapErr := snapshotSetupRC(target)
								if snapErr != nil {
									fmt.Printf("    Cannot snapshot %s: %v\n", target, snapErr)
									allGood = false
									continue
								}
								rollback = append(rollback, snapshot.restore)
							}
							if err := shims.InjectPath(target, shimDir); err != nil {
								fmt.Printf("    Cannot configure shim PATH in %s: %v\n", target, err)
								allGood = false
							}
						}
					}
				}
			}
		}
	}

	// 8. Configure git credential helper (opt-in)
	if credHelper && !settings.Native {
		fmt.Println()
		fmt.Print("  Configuring credential helper... ")
		binPath, err := resolveBinaryPath()
		if err != nil {
			fmt.Println("FAILED")
			return err
		} else if _, err := gitcred.Install(binPath, helperRealGH); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("cannot install Git helper: %w", err)
		} else if err := gitcred.ConfigureHosts(helperHosts, helperPathHosts...); err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("cannot configure Git helper: %w", err)
		} else {
			fmt.Println("OK")
			fmt.Println("    Git will now use gh-autoaccount for GitHub credentials.")
			fmt.Println("    This works in systemd timers, cron jobs, scripts, and IDEs.")
		}
	}

	if settings.GitIdentity && !migrationRequested {
		registry, err := config.LoadPins()
		if err != nil {
			return err
		}
		if err := gitidentity.Sync(registry); err != nil {
			return err
		}
		fmt.Println("  Git identity include registered (local config and environment can override).")
	}

	if migrationRequested {
		fmt.Print("  Running migration.......... ")
		report, err := runMigration()
		if err != nil {
			fmt.Println("FAILED")
			return fmt.Errorf("migration failed: %w", err)
		} else {
			fmt.Println("OK")
			fmt.Printf("    Pins updated: %d, .envrc rewritten: %d, direnv allow: %d\n", report.PinsUpdated, report.EnvrcRewritten, report.Allowed)
			for _, path := range report.ReviewRequired {
				fmt.Printf("    Review required: %s (run direnv allow after reviewing the full file)\n", path)
			}
			for _, path := range report.WritabilityReview {
				fmt.Printf("    Review required: %s (directory or original/rewritten .envrc permissions require manual review; run direnv allow after reviewing)\n", path)
			}
			for _, msg := range report.PermRepairsFailed {
				fmt.Printf("    Warning: could not repair permissions on %s\n", msg)
			}
			if report.Warnings > 0 {
				fmt.Printf("    Warnings: %d (missing/unwritable directories or manual .envrc review required)\n", report.Warnings)
				allGood = false
			}
		}
	}

	// Summary
	fmt.Println()
	if allGood {
		fmt.Println("  Setup complete! Pin accounts to directories with:")
		fmt.Println("    gh autoaccount pin <username> --dir <path>")
		fmt.Println()
		fmt.Println("  Token mode:")
		if settings.Native {
			fmt.Println("    native             — gh proxy and Git HTTPS helper use registry pins; no shell wrappers")
			fmt.Println("    export             — requires direnv; GH_TOKEN exported (use --export-token flag)")
			fmt.Println("    Git identity       — separately opt-in with --git-identity")
		} else {
			fmt.Println("    wrapper            — deprecated; token injected per-command, never in env")
			fmt.Println("    export             — deprecated; GH_TOKEN exported (use --export-token flag)")
			if cmd.Flags().Changed("native") {
				fmt.Println("    Git helper and managed identity settings are retained; the helper still routes HTTPS credentials via pins.")
			}
		}
		if installShim && !settings.Native {
			fmt.Println("    gh shim            - installed; requires PATH integration for plain gh")
		}
		if credHelper {
			fmt.Println("    git helper         — plain git HTTPS uses the pinned account automatically")
		}
		fmt.Println()
		if credHelper {
			fmt.Println("  Credential helper:")
			fmt.Println("    Git will resolve GitHub credentials from pins.yml automatically.")
			fmt.Println("    Works in systemd timers, cron jobs, scripts, and IDEs.")
			fmt.Println()
		} else if !gitcred.CheckConfigured() {
			fmt.Println("  Tip: Enable non-interactive auth (systemd, cron, IDEs) with:")
			fmt.Println("    gh autoaccount setup --credential-helper")
			fmt.Println()
		}
		if migrate {
			fmt.Println("  Migration complete.")
		}
		if settings.ExternalShell {
			fmt.Println("  External shell integration is not verified; reload it through your shell manager.")
		} else if rcPath != "" {
			fmt.Println("  Restart your shell or run: source " + rcPath)
		}
	} else {
		fmt.Println("  Setup complete with warnings (see above).")
		if cmd.Flags().Changed("native") && !settings.Native {
			fmt.Println("  The Git helper and identity settings are retained; the helper still routes HTTPS credentials via pins.")
		}
		fmt.Println("  Fix the warnings, then pin accounts with:")
		fmt.Println("    gh autoaccount pin <username> --dir <path>")
	}

	committed = true
	if alias, _ := cmd.Flags().GetBool("alias"); alias {
		realGH, err := ghauth.ResolveGHPath()
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not configure gh aa (%v); use gh autoaccount directly.\n", err)
		} else if created, err := configureShortAlias(cmd.Context(), realGH); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: could not configure gh aa (%v); use gh autoaccount directly.\n", err)
		} else if created {
			fmt.Fprintln(cmd.OutOrStdout(), "Configured gh aa alias.")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "gh aa alias is already configured.")
		}
	}
	return nil
}

// shimDirWinsForGH reports whether plain gh already resolves inside the shim
// directory. Being on PATH is not enough: an earlier gh (Homebrew, a distro
// package, a version manager) would still win.
func shimDirWinsForGH() bool {
	shimDir, err := shims.Dir()
	if err != nil {
		return false
	}
	resolved, err := exec.LookPath("gh")
	if err != nil {
		return false
	}
	// Compare physical directories: HOME or PATH entries may be symlinked spellings.
	got, gotErr := filepath.EvalSymlinks(filepath.Dir(resolved))
	want, wantErr := filepath.EvalSymlinks(shimDir)
	return gotErr == nil && wantErr == nil && got == want
}

// legacyDeprecation is shown wherever legacy (non-native) routing is active.
const legacyDeprecation = "legacy wrapper routing is deprecated: it does not cover non-interactive shells (agents, editor tasks, cron). Run `gh autoaccount setup --native` to migrate."

// nativeGHSupported is a seam for tests; legacy is the only mode where false.
var nativeGHSupported = shims.NativeGHSupported

// legacyNotice is empty where native routing is unavailable, so users are not
// told to run a command that cannot succeed.
func legacyNotice() string {
	if !nativeGHSupported() {
		return ""
	}
	return legacyDeprecation
}

// freshInstall reports whether no gh-autoaccount integration exists yet, so a
// default can be chosen without changing an existing installation. Uncertainty
// counts as installed: an unreadable path must never flip a legacy setup.
// settings.yml is not consulted because `default` can create it before setup.
func freshInstall() bool {
	for _, locate := range []func() (string, error){config.PinsFilePath, direnvlib.ShellHookPath, direnvlib.ShellLibPath} {
		path, err := locate()
		if err != nil {
			return false
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return false
		}
	}
	return !shims.IsGHInstalled() && !shims.IsNativeGHInstalled() && !gitcred.IsInstalled() &&
		!direnvlib.CheckShellHookInstalled() && !shims.CheckPathBlockInstalled()
}

type setupFileSnapshot struct {
	path   string
	mode   os.FileMode
	data   []byte
	target string
	exists bool
}

func snapshotSetupRC(path string) (setupFileSnapshot, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return setupFileSnapshot{}, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return setupFileSnapshot{}, err
	}
	return snapshotSetupFile(path)
}

func snapshotSetupFile(path string) (setupFileSnapshot, error) {
	s := setupFileSnapshot{path: path}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("cannot inspect %s: %w", path, err)
	}
	s.exists, s.mode = true, info.Mode()
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		s.target, err = os.Readlink(path)
	case info.Mode().IsRegular():
		s.data, err = os.ReadFile(path)
	case info.IsDir():
		// Atomic file replacement cannot replace a directory.
	default:
		return s, fmt.Errorf("cannot snapshot non-regular setup path %s", path)
	}
	if err != nil {
		return s, fmt.Errorf("cannot snapshot %s: %w", path, err)
	}
	return s, nil
}

func (s setupFileSnapshot) restore() (retErr error) {
	if s.mode.IsDir() {
		return nil
	}
	if !s.exists {
		if err := os.Remove(s.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cannot remove setup artifact %s: %w", s.path, err)
		}
		return nil
	}
	if s.mode&os.ModeSymlink == 0 {
		if err := config.WriteAtomicMode(s.path, s.data, s.mode&(os.ModePerm|os.ModeSetuid|os.ModeSetgid|os.ModeSticky)); err != nil {
			return fmt.Errorf("cannot restore %s: %w", s.path, err)
		}
		return nil
	}
	dir, err := os.MkdirTemp(filepath.Dir(s.path), ".autoprofile-restore-")
	if err != nil {
		return fmt.Errorf("cannot restore %s: %w", s.path, err)
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("cannot remove rollback temporary directory %s: %w", dir, err))
		}
	}()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(s.target, link); err != nil {
		return fmt.Errorf("cannot restore %s: %w", s.path, err)
	}
	if err := os.Rename(link, s.path); err != nil {
		return fmt.Errorf("cannot restore %s: %w", s.path, err)
	}
	return nil
}

type migrationReport struct {
	PinsUpdated       int
	EnvrcRewritten    int
	Allowed           int
	Warnings          int
	ReviewRequired    []string
	WritabilityReview []string
	PermRepairsFailed []string
}

func runMigration() (migrationReport, error) {
	var report migrationReport
	settings, err := config.LoadSettings()
	if err != nil {
		return report, fmt.Errorf("cannot load setup settings: %w", err)
	}

	registry, err := config.LoadPins()
	if err != nil {
		return report, fmt.Errorf("cannot load pin registry: %w", err)
	}
	if settings.Native {
		for _, pin := range registry.Pins {
			if !pin.IsOwner() && pin.EffectiveMode() == config.ModeWrapper {
				if err := direnvlib.RequireManualEnvrcCleanup(pin.Dir); err != nil {
					return report, err
				}
			}
		}
	}

	configDir, err := config.ConfigDir()
	if err == nil {
		if chmodErr := os.Chmod(configDir, 0700); chmodErr != nil {
			report.Warnings++
			report.PermRepairsFailed = append(report.PermRepairsFailed, fmt.Sprintf("%s (want 0700): %v", configDir, chmodErr))
		}
	}
	if pinsPath, err := config.PinsFilePath(); err == nil {
		if _, statErr := os.Stat(pinsPath); statErr == nil {
			if chmodErr := os.Chmod(pinsPath, 0600); chmodErr != nil {
				report.Warnings++
				report.PermRepairsFailed = append(report.PermRepairsFailed, fmt.Sprintf("%s (want 0600): %v", pinsPath, chmodErr))
			}
		}
	}

	updatedMode := 0
	for i := range registry.Pins {
		if registry.Pins[i].Mode == "" {
			registry.Pins[i].Mode = config.ModeWrapper
			updatedMode++
		}
	}
	report.PinsUpdated = updatedMode
	if err := savePinsAndIdentity(registry); err != nil {
		return report, fmt.Errorf("cannot migrate pins and Git identity: %w", err)
	}

	for _, pin := range registry.Pins {
		if pin.IsOwner() {
			continue
		}
		if _, err := os.Stat(pin.Dir); err != nil {
			report.Warnings++
			continue
		}
		if settings.Native && pin.EffectiveMode() == config.ModeWrapper {
			continue
		}
		hasUnmanagedContent, err := direnvlib.HasUnmanagedEnvrcContent(pin.Dir)
		if err != nil {
			report.Warnings++
			report.ReviewRequired = append(report.ReviewRequired, filepath.Join(pin.Dir, ".envrc"))
			continue
		}

		autoAllowBefore, autoAllowErr := direnvlib.AutoAllowSafe(pin.Dir)
		snapshot, err := direnvlib.WriteEnvrcSnapshot(pin)
		if err != nil {
			report.Warnings++
			continue
		}
		report.EnvrcRewritten++
		if hasUnmanagedContent {
			report.Warnings++
			report.ReviewRequired = append(report.ReviewRequired, filepath.Join(pin.Dir, ".envrc"))
			continue
		}

		// Recheck after rewriting, without forgetting unsafe original permissions.
		if direnvlib.HasUnmanagedContent(snapshot) {
			report.Warnings++
			report.ReviewRequired = append(report.ReviewRequired, filepath.Join(pin.Dir, ".envrc"))
			continue
		}
		autoAllowAfter, gateErr := direnvlib.AutoAllowSafe(pin.Dir)
		if autoAllowErr != nil || !autoAllowBefore || gateErr != nil || !autoAllowAfter {
			report.Warnings++
			report.WritabilityReview = append(report.WritabilityReview, filepath.Join(pin.Dir, ".envrc"))
			continue
		}

		if err := direnvlib.AllowSnapshot(pin.Dir, snapshot); err != nil {
			report.Warnings++
			report.ReviewRequired = append(report.ReviewRequired, filepath.Join(pin.Dir, ".envrc"))
			continue
		}
		report.Allowed++
	}

	return report, nil
}

// loginProfile returns the login-shell startup file for an interactive RC
// file. Login shells (bash -l, zsh -l, macOS terminals, many agent tools) skip
// the interactive RC file. Bash reads the first of these that exists, so a
// dangling symlink does not count.
func loginProfile(rcPath string) string {
	dir := filepath.Dir(rcPath)
	switch filepath.Base(rcPath) {
	case ".zshrc":
		return filepath.Join(dir, ".zprofile")
	case ".bashrc":
		for _, name := range []string{".bash_profile", ".bash_login", ".profile"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return filepath.Join(dir, name)
			}
		}
		return filepath.Join(dir, ".profile")
	}
	return ""
}

// detectShellRC finds the user's active shell RC file.
func detectShellRC() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	// Check SHELL env var first.
	shell := os.Getenv("SHELL")
	if strings.HasSuffix(shell, "/zsh") {
		return filepath.Join(home, ".zshrc"), nil
	}
	if strings.HasSuffix(shell, "/bash") {
		return filepath.Join(home, ".bashrc"), nil
	}

	// Fallback: check which RC files exist.
	for _, name := range []string{".zshrc", ".bashrc", ".bash_profile", ".profile"} {
		p := filepath.Join(home, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("could not detect shell RC file (SHELL=%s)", shell)
}

// isVersionAtLeast compares semver strings (major.minor.patch).
func isVersionAtLeast(current, minimum string) bool {
	cParts := strings.Split(current, ".")
	mParts := strings.Split(minimum, ".")

	for i := 0; i < 3; i++ {
		var c, m int
		if i < len(cParts) {
			fmt.Sscanf(cParts[i], "%d", &c)
		}
		if i < len(mParts) {
			fmt.Sscanf(mParts[i], "%d", &m)
		}
		if c > m {
			return true
		}
		if c < m {
			return false
		}
	}
	return true // equal
}

// resolveBinaryPath returns the absolute path of the gh-autoaccount binary.
// It first tries os.Executable() (the running binary), then falls back to
// exec.LookPath to find it on $PATH.
func resolveBinaryPath() (string, error) {
	// Prefer the running binary — it's always correct even if $PATH hasn't
	// been updated after an install/upgrade.
	exePath, err := os.Executable()
	if err == nil {
		// Resolve symlinks so the gitconfig entry survives link rotation
		// (e.g., Homebrew cellar symlinks).
		resolved, err := filepath.EvalSymlinks(exePath)
		if err == nil {
			return resolved, nil
		}
		return exePath, nil
	}

	// Fallback: search $PATH.
	p, err := exec.LookPath("gh-autoaccount")
	if err != nil {
		return "", fmt.Errorf("cannot find gh-autoaccount binary: %w", err)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p, nil
	}
	return abs, nil
}
