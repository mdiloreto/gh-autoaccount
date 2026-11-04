package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
	"github.com/spf13/cobra"
)

// NewUnpinCmd creates the `unpin` subcommand.
func NewUnpinCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "unpin [directory]",
		Short: "Remove a pinned account from a directory or owner",
		Long: `Remove the gh-autoprofile block from the directory's .envrc and
delete the pin from the registry. If the .envrc has no other content,
it will be deleted entirely.

With --owner, remove the owner pin instead; no .envrc is involved.

Examples:
  gh autoaccount unpin              # unpin current directory
  gh autoaccount unpin ~/project    # unpin specific directory
  gh autoaccount unpin --owner work-org`,
		Args: cobra.MaximumNArgs(1),
		RunE: runUnpin,
	}
	cmd.Flags().String("owner", "", "Remove the pin for this repository owner instead of a directory")
	cmd.Flags().String("host", "", "GitHub hostname of the owner pin (with --owner)")
	return cmd
}

func runUnpin(cmd *cobra.Command, args []string) error {
	owner, _ := cmd.Flags().GetString("owner")
	host, _ := cmd.Flags().GetString("host")
	if owner != "" {
		if len(args) > 0 {
			return fmt.Errorf("--owner cannot be combined with a directory argument")
		}
		return runUnpinOwner(owner, host)
	}
	if host != "" {
		return fmt.Errorf("--host requires --owner")
	}
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("cannot resolve directory: %w", err)
	}

	// Load registry
	unlock, err := config.LockMutations()
	if err != nil {
		return err
	}
	defer unlock()
	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pin registry: %w", err)
	}

	// Check if directory is pinned
	var pin *config.Pin
	canonical, canonicalErr := config.CanonicalDir(absDir)
	if canonicalErr == nil {
		selected, findErr := registry.FindPinStrict(canonical)
		if findErr != nil {
			return findErr
		}
		if selected != nil {
			selectedDir, err := config.CanonicalDir(selected.Dir)
			if err == nil && selectedDir == canonical {
				pin = selected
			}
		}
	} else {
		for i := range registry.Pins {
			if registry.Pins[i].Dir == absDir {
				pin = &registry.Pins[i]
				break
			}
		}
	}
	if pin == nil {
		return fmt.Errorf("no pin found for directory: %s", absDir)
	}
	absDir = pin.Dir
	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("cannot load setup settings: %w", err)
	}
	native := settings.Native && pin.EffectiveMode() == config.ModeWrapper

	user := pin.User
	previous := &config.PinRegistry{Pins: append([]config.Pin(nil), registry.Pins...)}
	var snapshot setupFileSnapshot
	cleaned := false
	_, dirErr := os.Stat(absDir)
	if dirErr != nil && !os.IsNotExist(dirErr) {
		return fmt.Errorf("cannot inspect directory %s: %w", absDir, dirErr)
	}
	if dirErr == nil && native {
		if err := direnvlib.RequireManualEnvrcCleanup(absDir); err != nil {
			return err
		}
	} else if dirErr == nil {
		snapshot, err = snapshotSetupFile(filepath.Join(absDir, ".envrc"))
		if err != nil {
			return err
		}
		cleaned, err = direnvlib.CleanupManagedEnvrc(absDir)
		if err != nil {
			return fmt.Errorf("cannot clean .envrc: %w", err)
		}
	}
	restoreEnvrc := func() error {
		if !cleaned {
			return nil
		}
		return errors.Join(fmt.Errorf("authorization was revoked; review the restored .envrc and run direnv allow manually if appropriate"), snapshot.restore())
	}

	// Remove from registry
	registry.RemovePin(absDir)
	if err := savePinsAndIdentity(registry); err != nil {
		return errors.Join(fmt.Errorf("cannot remove pin and Git identity: %w", err), restoreEnvrc())
	}
	// Removed hosts retain their prior section: no historical helper snapshot exists.
	if _, err := refreshGitHelpers(registry, previous); err != nil {
		return errors.Join(err, restoreEnvrc())
	}
	if dirErr != nil {
		fmt.Printf("Unpinned '%s' from %s (directory inaccessible; .envrc cleanup skipped)\n", user, absDir)
		return nil
	}

	if cleaned {
		fmt.Println("Reload your shell to clear any previously loaded exports.")
	}

	fmt.Printf("Unpinned '%s' from %s\n", user, absDir)
	return nil
}

func runUnpinOwner(owner, host string) error {
	unlock, err := config.LockMutations()
	if err != nil {
		return err
	}
	defer unlock()
	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pin registry: %w", err)
	}
	pin := registry.FindOwnerPin(host, owner)
	if pin == nil {
		return fmt.Errorf("no owner pin found for %s", owner)
	}
	user := pin.User
	previous := &config.PinRegistry{Pins: append([]config.Pin(nil), registry.Pins...)}
	registry.RemoveOwnerPin(host, owner)
	if err := savePinsAndIdentity(registry); err != nil {
		return fmt.Errorf("cannot remove pin and Git identity: %w", err)
	}
	if _, err := refreshGitHelpers(registry, previous); err != nil {
		return err
	}
	fmt.Printf("Unpinned '%s' from owner %s\n", user, owner)
	return nil
}
