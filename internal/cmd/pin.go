package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/spf13/cobra"
)

// NewPinCmd creates the `pin` subcommand.
func NewPinCmd() *cobra.Command {
	var dir, owner, gitEmail, gitName, sshKey, host, tokenCommand string
	var exportToken bool

	cmd := &cobra.Command{
		Use:   "pin <username>",
		Short: "Pin a GitHub account to a directory",
		Long: `Pin a GitHub account to a directory. When you cd into the directory,
the correct credentials and git identity are automatically activated.

By default, tokens are injected per-command via shell wrapper functions
(wrapper mode). The token never sits in your shell environment.

Use --export-token for directories where third-party tools need GH_TOKEN /
GITHUB_TOKEN as environment variables.

Use --owner to pin a GitHub organization or user instead of a directory: any
gh or Git HTTPS access to that owner's repositories uses this account, and
outranks the directory pin. Owner pins are wrapper-mode only.

Examples:
  gh autoaccount pin alice
  gh autoaccount pin bob-work --dir ~/work --git-email bob@example.test
  gh autoaccount pin alice-freelance --dir ~/freelance --export-token
  gh autoaccount pin bob-work --owner work-org --git-email bob@example.test`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if owner != "" {
				return runPinOwner(args[0], owner, gitEmail, gitName, sshKey, host, tokenCommand, exportToken)
			}
			return runPinWithHost(args[0], dir, gitEmail, gitName, sshKey, host, tokenCommand, exportToken)
		},
	}

	cmd.Flags().StringVarP(&dir, "dir", "d", ".", "Directory to pin (defaults to current directory)")
	cmd.Flags().StringVar(&owner, "owner", "", "Pin a repository owner (organization or user) instead of a directory")
	cmd.MarkFlagsMutuallyExclusive("dir", "owner")
	cmd.Flags().StringVar(&gitEmail, "git-email", "", "Git author/committer email for this directory")
	cmd.Flags().StringVar(&gitName, "git-name", "", "Git author/committer name for this directory")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "Path to SSH private key for this directory")
	cmd.Flags().StringVar(&host, "host", "", "GitHub hostname")
	cmd.Flags().StringVar(&tokenCommand, "token-command", "", "Absolute path to credential broker")
	cmd.Flags().BoolVar(&exportToken, "export-token", false, "Export GH_TOKEN/GITHUB_TOKEN into the shell environment (less secure)")

	return cmd
}

func runPin(user, dir, gitEmail, gitName, sshKey string, exportToken bool) error {
	return runPinWithHost(user, dir, gitEmail, gitName, sshKey, "", "", exportToken)
}

func runPinWithHost(user, dir, gitEmail, gitName, sshKey, host, tokenCommand string, exportToken bool) error {
	// Resolve absolute path
	absDir, err := config.CanonicalDir(dir)
	if err != nil {
		return fmt.Errorf("cannot resolve directory: %w", err)
	}

	// Validate directory exists
	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("directory does not exist: %s", absDir)
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", absDir)
	}

	host, err = resolvePinCredential(user, host, tokenCommand)
	if err != nil {
		return err
	}
	if sshKey, err = canonicalSSHKey(sshKey); err != nil {
		return err
	}

	// Determine mode
	mode := config.ModeWrapper
	if exportToken {
		mode = config.ModeExport
	}

	// Create pin
	pin := config.Pin{
		User:         user,
		Host:         host,
		TokenCommand: tokenCommand,
		Dir:          absDir,
		Mode:         mode,
		GitEmail:     gitEmail,
		GitName:      gitName,
		SSHKey:       sshKey,
	}

	// Save to registry
	unlock, err := config.LockMutations()
	if err != nil {
		return err
	}
	defer unlock()
	settings, err := config.LoadSettings()
	if err != nil {
		return fmt.Errorf("cannot load setup settings: %w", err)
	}
	native := settings.Native && mode == config.ModeWrapper
	if !native && !direnvlib.IsShellLibInstalled() {
		return fmt.Errorf("direnv shell library not installed. Run first: gh autoaccount setup")
	}
	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pin registry: %w", err)
	}
	previous := &config.PinRegistry{Pins: append([]config.Pin(nil), registry.Pins...)}
	if err := registry.AddPinStrict(pin); err != nil {
		return err
	}
	if native {
		if err := direnvlib.RequireManualEnvrcCleanup(absDir); err != nil {
			return err
		}
	}
	if err := savePinsAndIdentity(registry); err != nil {
		return fmt.Errorf("cannot save pin and Git identity: %w", err)
	}
	var helpers gitcred.HelperSnapshot
	if gitcred.CheckConfigured() {
		hosts, err := gitcred.Hosts(registry)
		if err == nil {
			helpers, err = gitcred.SnapshotHosts(hosts)
		}
		if err == nil {
			err = gitcred.ConfigureHosts(hosts)
		}
		if err != nil {
			return errors.Join(fmt.Errorf("cannot refresh Git helpers: %w", err), gitcred.RestoreHosts(helpers), savePinsAndIdentity(previous))
		}
	}

	if !native {
		// Write .envrc
		autoAllowBefore, autoAllowErr := direnvlib.AutoAllowSafe(absDir)
		snapshot, err := direnvlib.WriteEnvrcSnapshot(pin)
		if err != nil {
			return errors.Join(fmt.Errorf("cannot write .envrc: %w", err), savePinsAndIdentity(previous), gitcred.RestoreHosts(helpers))
		}

		// Auto-allow .envrc — but never on the user's behalf when content exists
		// outside the managed block or local permissions permit concurrent writers.
		if direnvlib.IsInstalled() {
			if direnvlib.HasUnmanagedContent(snapshot) {
				fmt.Printf("Warning: .envrc contains unmanaged content — review before authorising.\n")
				fmt.Printf("  Run manually: direnv allow %s/.envrc\n", absDir)
			} else if !autoAllowBefore || autoAllowErr != nil {
				fmt.Printf("Warning: directory or original .envrc permissions require manual review before authorising.\n")
				if autoAllowErr != nil {
					fmt.Printf("  Cannot inspect permissions: %v\n", autoAllowErr)
				}
				fmt.Printf("  Run manually after reviewing: direnv allow %s/.envrc\n", absDir)
			} else if safe, gateErr := direnvlib.AutoAllowSafe(absDir); gateErr != nil || !safe {
				fmt.Printf("Warning: directory or rewritten .envrc permissions require manual review before authorising.\n")
				if gateErr != nil {
					fmt.Printf("  Cannot inspect permissions: %v\n", gateErr)
				}
				fmt.Printf("  Run manually after reviewing: direnv allow %s/.envrc\n", absDir)
			} else if err := direnvlib.AllowSnapshot(absDir, snapshot); err != nil {
				fmt.Printf("Warning: could not auto-allow .envrc: %v\n", err)
				fmt.Printf("  Run manually: direnv allow %s/.envrc\n", absDir)
			}
		}
	}

	// Summary
	modeLabel := "wrapper"
	if mode == config.ModeExport {
		modeLabel = "export"
	}
	fmt.Printf("\nPinned '%s' -> %s\n", user, absDir)
	fmt.Printf("  Mode:       %s\n", modeLabel)
	if gitEmail != "" {
		fmt.Printf("  Git email:  %s\n", gitEmail)
	}
	if gitName != "" {
		fmt.Printf("  Git name:   %s\n", gitName)
	}
	if sshKey != "" {
		fmt.Printf("  SSH key:    %s\n", sshKey)
	}
	if !native {
		fmt.Printf("  .envrc:     %s/.envrc\n", absDir)
	}

	if mode == config.ModeWrapper {
		fmt.Println("\n  Token is injected per-command (never in shell environment).")
	} else {
		fmt.Println("\n  WARNING: Token is exported into shell environment.")
		fmt.Println("  All child processes can read GH_TOKEN/GITHUB_TOKEN.")
	}
	fmt.Println("\ncd into the directory to activate the profile.")

	return nil
}

// resolvePinCredential resolves the configured credential without claiming
// remote identity verification, and returns the normalized explicit host.
func resolvePinCredential(user, host, tokenCommand string) (string, error) {
	fmt.Printf("Resolving credential for '%s'... ", user)
	if host != "" || tokenCommand != "" {
		normalized, err := config.NormalizeHost(host)
		if err != nil {
			return "", err
		}
		if host != "" {
			host = normalized
		}
		_, err = ghauth.ResolveToken(context.Background(), ghauth.TokenRequest{User: user, Host: host, TokenCommand: tokenCommand})
		if err != nil {
			fmt.Println("FAILED")
			return "", err
		}
	} else if err := ghauth.ValidateUser(user); err != nil {
		fmt.Println("FAILED")
		return "", err
	}
	fmt.Println("OK (remote identity not verified)")
	return host, nil
}

// canonicalSSHKey makes an optional SSH key path absolute and checks it exists.
func canonicalSSHKey(sshKey string) (string, error) {
	if sshKey == "" {
		return "", nil
	}
	if absKey, err := filepath.Abs(sshKey); err == nil {
		sshKey = absKey
	}
	if _, err := os.Stat(sshKey); err != nil {
		return "", fmt.Errorf("SSH key not found: %s", sshKey)
	}
	return sshKey, nil
}

// refreshGitHelpers reconfigures managed Git host sections when the helper is
// active. On failure it restores the helpers and the previous registry.
func refreshGitHelpers(registry, previous *config.PinRegistry) error {
	if !gitcred.CheckConfigured() {
		return nil
	}
	var helpers gitcred.HelperSnapshot
	hosts, err := gitcred.Hosts(registry)
	if err == nil {
		helpers, err = gitcred.SnapshotHosts(hosts)
	}
	if err == nil {
		err = gitcred.ConfigureHosts(hosts)
	}
	if err != nil {
		return errors.Join(fmt.Errorf("cannot refresh Git helpers: %w", err), gitcred.RestoreHosts(helpers), savePinsAndIdentity(previous))
	}
	return nil
}

// runPinOwner pins an account to a repository owner. Owner pins never touch
// .envrc or direnv: they are selected from the registry per command.
func runPinOwner(user, owner, gitEmail, gitName, sshKey, host, tokenCommand string, exportToken bool) error {
	if exportToken {
		return fmt.Errorf("--export-token is not supported with --owner; owner pins use wrapper mode")
	}
	pin := config.Pin{User: user, Host: host, TokenCommand: tokenCommand, Owner: owner, Mode: config.ModeWrapper, GitEmail: gitEmail, GitName: gitName}
	if err := new(config.PinRegistry).AddOwnerPin(pin); err != nil {
		return err
	}
	host, err := resolvePinCredential(user, host, tokenCommand)
	if err != nil {
		return err
	}
	pin.Host = host
	if pin.SSHKey, err = canonicalSSHKey(sshKey); err != nil {
		return err
	}

	unlock, err := config.LockMutations()
	if err != nil {
		return err
	}
	defer unlock()
	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pin registry: %w", err)
	}
	previous := &config.PinRegistry{Pins: append([]config.Pin(nil), registry.Pins...)}
	if err := registry.AddOwnerPin(pin); err != nil {
		return err
	}
	if err := savePinsAndIdentity(registry); err != nil {
		return fmt.Errorf("cannot save pin and Git identity: %w", err)
	}
	if err := refreshGitHelpers(registry, previous); err != nil {
		return err
	}

	displayHost, _ := config.NormalizeHost(pin.Host)
	fmt.Printf("\nPinned '%s' -> owner %s on %s\n", user, owner, displayHost)
	if gitEmail != "" {
		fmt.Printf("  Git email:  %s\n", gitEmail)
	}
	if gitName != "" {
		fmt.Printf("  Git name:   %s\n", gitName)
	}
	if pin.SSHKey != "" {
		fmt.Printf("  SSH key:    %s\n", pin.SSHKey)
	}
	fmt.Println("\n  Token is injected per-command (never in shell environment).")
	if !gitcred.CheckConfigured() {
		fmt.Println("  Plain git HTTPS needs the credential helper: gh autoaccount setup --credential-helper")
	}
	return nil
}
