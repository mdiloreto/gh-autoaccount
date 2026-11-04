package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/gitidentity"
	"github.com/mdiloreto/gh-autoaccount/internal/legacy"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
	"github.com/spf13/cobra"
)

// legacyRemoved names the replacement for the retired direnv modes.
const legacyRemoved = "legacy wrapper and export routing were removed; native routing covers every shell, and `gh autoaccount exec -- <command>` gives a tool a command-scoped token"

// NewSetupCmd creates the `setup` subcommand.
func NewSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install native gh routing and the Git HTTPS credential helper",
		Long: `Checks that gh CLI (>= 2.40.0) is installed, then installs:

  1. A PATH-level gh proxy that routes plain gh per pin
  2. The GitHub HTTPS credential helper that routes Git per pin
  3. Optionally, Git-native path-scoped identity (--git-identity)
  4. Optionally, mise compatibility (--mise)

Setup also removes what the retired direnv wrapper and export modes left
behind: managed .envrc blocks (after direnv deny), the direnv library, the shell
hook and its RC source block. Content it cannot safely edit is reported for
manual review.

Use --no-shell-rc for externally managed shell integration. This preference is
persisted: future setup, --migrate, and doctor --fix leave RC and PATH untouched.
Use --no-shell-rc=false to return to automatic RC integration.

Run this once after installing gh-autoaccount, and with --migrate after
upgrading to refresh generated files, repair permissions, and update existing
pins to the latest defaults.`,
		RunE: runSetup,
	}
	cmd.Flags().Bool("migrate", false, "Refresh generated files and migrate existing pins")
	cmd.Flags().Bool("no-shell-rc", false, "Persist external shell integration; do not edit shell RC or PATH")
	cmd.Flags().Bool("git-identity", false, "Enable managed Git includeIf identity from pins (persists across migration)")
	cmd.Flags().Bool("mise", false, "Keep plain gh on the managed shim when mise manages gh (persisted); false undoes it")
	cmd.Flags().Bool("alias", false, "Optionally configure the non-clobbering gh aa alias")
	// Accepted so existing scripts keep working; native routing always installs both.
	for _, name := range []string{"native", "gh-shim", "credential-helper"} {
		cmd.Flags().Bool(name, false, "")
		_ = cmd.Flags().MarkHidden(name)
	}
	return cmd
}

func runSetup(cmd *cobra.Command, args []string) (retErr error) {
	if native, _ := cmd.Flags().GetBool("native"); cmd.Flags().Changed("native") && !native {
		return errors.New(legacyRemoved)
	}
	if !shims.NativeGHSupported() {
		return fmt.Errorf("native gh routing is not supported on this platform yet")
	}
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
	// Persisted so that an older binary reading these settings also stays native.
	settings.Native = true
	miseChanged := cmd.Flags().Changed("mise")
	if miseChanged {
		settings.Mise, _ = cmd.Flags().GetBool("mise")
	}
	if settings.Mise {
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
	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pin registry: %w", err)
	}
	for _, pin := range registry.Pins {
		// Export or unset modes come from an older install and must be converted.
		migrationRequested = migrationRequested || pin.Mode != config.ModeWrapper
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

	// 2. Check logged-in accounts
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

	binPath, err := resolveBinaryPath()
	if err != nil {
		return err
	}
	helperRealGH, err := ghauth.ResolveGHPath()
	if err != nil {
		return fmt.Errorf("cannot resolve real gh binary for Git helper: %w", err)
	}
	helperHosts, err := gitcred.Hosts(registry)
	if err != nil {
		return err
	}
	helperPathHosts, err := gitcred.OwnerHosts(registry)
	if err != nil {
		return err
	}
	configDir, err := config.ConfigDir()
	if err != nil {
		return err
	}
	shimPath, err := shims.GHPath()
	if err != nil {
		return err
	}
	helperPath, err := gitcred.HelperPath()
	if err != nil {
		return err
	}
	if err := shims.CheckGHReplaceable(); err != nil {
		return err
	}
	// Snapshot every file and the Git helper chain that setup may replace, so a
	// failure before the commit point leaves the previous installation intact.
	committed := false
	var rollback []func() error
	var settingsSnapshot, shimSnapshot, recordSnapshot, helperSnapshot setupFileSnapshot
	for _, file := range []struct {
		path string
		dest *setupFileSnapshot
	}{
		{filepath.Join(configDir, "settings.yml"), &settingsSnapshot},
		{shimPath, &shimSnapshot},
		{filepath.Join(configDir, "native-gh-targets.json"), &recordSnapshot},
		{helperPath, &helperSnapshot},
	} {
		if *file.dest, err = snapshotSetupFile(file.path); err != nil {
			return err
		}
	}
	priorHelpers, err := gitcred.SnapshotHosts(helperHosts)
	if err != nil {
		return fmt.Errorf("cannot inspect Git helper configuration: %w", err)
	}
	defer func() {
		if retErr == nil || committed {
			return
		}
		for i := len(rollback) - 1; i >= 0; i-- {
			retErr = errors.Join(retErr, rollback[i]())
		}
		retErr = errors.Join(retErr, gitcred.RestoreHosts(priorHelpers), helperSnapshot.restore())
	}()
	configureMise := func() error {
		fmt.Print("  Configuring mise............ ")
		warning, err := applyMise(settings.Mise, binPath, func(undo func() error) { rollback = append(rollback, undo) })
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
	// Disabling runs before the proxy is replaced: removal only touches a
	// gh-only shim whose ownership record is still in place.
	if miseChanged && !settings.Mise {
		if err := configureMise(); err != nil {
			return err
		}
	}
	// Native routing requires both the proxy and Git helper before it is persisted.
	rollback = append(rollback, func() error {
		if err := shims.CheckGHReplaceable(); err != nil {
			return fmt.Errorf("cannot safely restore gh proxy: %w", err)
		}
		return errors.Join(recordSnapshot.restore(), shimSnapshot.restore())
	})
	if _, err := shims.InstallNativeGH(binPath); err != nil {
		return fmt.Errorf("cannot install native gh proxy: %w", err)
	}
	if _, err := gitcred.Install(binPath, helperRealGH); err != nil {
		return fmt.Errorf("cannot install required Git credential helper: %w", err)
	}
	if err := gitcred.ConfigureHosts(helperHosts, helperPathHosts...); err != nil {
		return fmt.Errorf("cannot configure required Git credential helper: %w", err)
	}
	if settings.Mise {
		if err := configureMise(); err != nil {
			return err
		}
	}
	if err := config.SaveSettings(settings); err != nil {
		return fmt.Errorf("cannot save setup settings: %w", err)
	}
	rollback = append(rollback, settingsSnapshot.restore)

	// 3. Put the shim directory first on PATH in the shell startup files.
	fmt.Println()
	fmt.Print("  Configuring shell PATH...... ")
	rcPath := ""
	shimDir, err := shims.Dir()
	if err != nil {
		return err
	}
	if settings.ExternalShell {
		fmt.Println("EXTERNAL (RC and PATH unchanged; integration not verified)")
		fmt.Printf("    Ensure your externally managed PATH puts %s before any other gh.\n", shimDir)
	} else if rcPath, err = detectShellRC(); err != nil {
		fmt.Println("SKIPPED")
		fmt.Printf("    %v\n", err)
		fmt.Printf("    Put %s before any other gh on PATH in your shell startup files.\n", shimDir)
		allGood = false
	} else if !shims.CheckPathBlockInstalled() && shimDirWinsForGH() {
		// A user who already puts the shim first without our block manages PATH.
		fmt.Println("OK (shim already first on PATH)")
	} else {
		configured := true
		for _, target := range []string{rcPath, loginProfile(rcPath)} {
			if target == "" || shims.HasPathBlock(target) {
				continue
			}
			snapshot, snapErr := snapshotSetupRC(target)
			if snapErr != nil {
				fmt.Printf("\n    Cannot snapshot %s: %v", target, snapErr)
				configured = false
				continue
			}
			rollback = append(rollback, snapshot.restore)
			if err := shims.InjectPath(target, shimDir); err != nil {
				fmt.Printf("\n    Cannot configure shim PATH in %s: %v", target, err)
				configured = false
			}
		}
		if configured {
			fmt.Println("OK")
		} else {
			fmt.Println()
			allGood = false
		}
	}

	if settings.GitIdentity && !migrationRequested {
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
		}
		fmt.Println("OK")
		fmt.Printf("    Pins updated: %d\n", report.PinsUpdated)
		for _, msg := range report.PermRepairsFailed {
			fmt.Printf("    Warning: could not repair permissions on %s\n", msg)
		}
		if len(report.PermRepairsFailed) > 0 {
			allGood = false
		}
	}
	committed = true

	// Cleanup of retired files runs after the commit point: direnv deny cannot be
	// rolled back, and native routing no longer depends on any of these files.
	if !cleanupLegacy(registry, settings.ExternalShell) {
		allGood = false
	}

	fmt.Println()
	if allGood {
		fmt.Println("  Setup complete! Pin accounts with:")
	} else {
		fmt.Println("  Setup complete with warnings (see above). Fix them, then pin accounts with:")
	}
	fmt.Println("    gh autoaccount pin <username> --dir <path>")
	fmt.Println()
	fmt.Println("  Plain gh and Git HTTPS now use the pinned account in every shell, including")
	fmt.Println("  agents, editor tasks and scripts started from it.")
	if migrate {
		fmt.Println("  Migration complete.")
	}
	if settings.ExternalShell {
		fmt.Println("  External shell integration is not verified; reload it through your shell manager.")
	} else if rcPath != "" {
		fmt.Println("  Restart your shell or run: source " + rcPath)
	}

	if alias, _ := cmd.Flags().GetBool("alias"); alias {
		if created, err := configureShortAlias(cmd.Context(), helperRealGH); err != nil {
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
	PermRepairsFailed []string
}

func runMigration() (migrationReport, error) {
	var report migrationReport
	registry, err := config.LoadPins()
	if err != nil {
		return report, fmt.Errorf("cannot load pin registry: %w", err)
	}

	configDir, err := config.ConfigDir()
	if err == nil {
		if chmodErr := os.Chmod(configDir, 0700); chmodErr != nil {
			report.PermRepairsFailed = append(report.PermRepairsFailed, fmt.Sprintf("%s (want 0700): %v", configDir, chmodErr))
		}
	}
	if pinsPath, err := config.PinsFilePath(); err == nil {
		if _, statErr := os.Stat(pinsPath); statErr == nil {
			if chmodErr := os.Chmod(pinsPath, 0600); chmodErr != nil {
				report.PermRepairsFailed = append(report.PermRepairsFailed, fmt.Sprintf("%s (want 0600): %v", pinsPath, chmodErr))
			}
		}
	}

	// Export pins become native pins; an empty mode predates explicit modes.
	for i := range registry.Pins {
		if registry.Pins[i].Mode != config.ModeWrapper {
			registry.Pins[i].Mode = config.ModeWrapper
			report.PinsUpdated++
		}
	}
	if err := savePinsAndIdentity(registry); err != nil {
		return report, fmt.Errorf("cannot migrate pins and Git identity: %w", err)
	}
	return report, nil
}

// cleanupLegacy removes what the retired direnv modes left behind and reports
// each step. It returns false when something needs the user's attention.
func cleanupLegacy(registry *config.PinRegistry, externalShell bool) bool {
	var cleaned int
	var review []string
	for _, pin := range registry.Pins {
		if pin.IsOwner() || !legacy.HasManagedEnvrc(pin.Dir) {
			continue
		}
		if ok, err := legacy.CleanupManagedEnvrc(pin.Dir); err != nil {
			review = append(review, fmt.Sprintf("%s: %v", filepath.Join(pin.Dir, ".envrc"), err))
		} else if ok {
			cleaned++
		}
	}
	var sourcedBy []string
	if rcFiles, err := legacy.RCFiles(); err == nil {
		for _, rc := range rcFiles {
			if !externalShell && legacy.HasHookSource(rc) {
				if _, err := legacy.RemoveHookSource(rc); err != nil {
					review = append(review, fmt.Sprintf("%s: %v", rc, err))
				} else {
					cleaned++
				}
			}
			// A line outside the managed block still sources the hook.
			if legacy.ReferencesHook(rc) {
				sourcedBy = append(sourcedBy, rc)
			}
		}
	}
	if removed, err := legacy.RemoveLibrary(); err != nil {
		review = append(review, err.Error())
	} else if removed {
		cleaned++
	}
	hookPath, _ := legacy.ShellHookPath()
	keptHook := ""
	if _, err := os.Lstat(hookPath); err == nil {
		if externalShell || len(sourcedBy) > 0 {
			// Deleting a file a shell still sources would break shell startup.
			keptHook = hookPath
		} else if removed, err := legacy.RemoveHook(); err != nil {
			review = append(review, err.Error())
		} else if removed {
			cleaned++
		}
	}
	clean := len(review) == 0 && keptHook == ""
	if cleaned == 0 && clean {
		return true
	}
	fmt.Print("  Removing legacy files....... ")
	if clean {
		fmt.Println("OK")
	} else {
		fmt.Println("REVIEW")
	}
	if cleaned > 0 {
		fmt.Printf("    Removed %d legacy item(s); restart open shells to drop old wrappers and exports.\n", cleaned)
	}
	for _, item := range review {
		fmt.Printf("    Review required: %s\n", item)
	}
	if keptHook != "" {
		fmt.Printf("    Remove the line that sources %s from your shell config, then delete that file.\n", keptHook)
		if len(sourcedBy) > 0 {
			fmt.Printf("    It is referenced from: %s\n", strings.Join(sourcedBy, ", "))
		}
	}
	return clean
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
