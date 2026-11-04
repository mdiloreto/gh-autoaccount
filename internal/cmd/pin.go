package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/mdiloreto/gh-autoaccount/internal/gitcred"
	"github.com/mdiloreto/gh-autoaccount/internal/legacy"
	"github.com/spf13/cobra"
)

// NewPinCmd creates the `pin` subcommand.
func NewPinCmd() *cobra.Command {
	var dir, owner, gitEmail, gitName, sshKey, host, tokenCommand string
	var exportToken bool

	cmd := &cobra.Command{
		Use:   "pin <username>",
		Short: "Pin a GitHub account to a directory",
		Long: `Pin a GitHub account to a directory. Plain gh and Git HTTPS in that
directory and below use the account, in every shell, including agents and cron.

Tokens are injected per-command and never sit in your shell environment. For a
third-party tool that reads GH_TOKEN, run it through gh autoaccount exec.

Use --owner to pin a GitHub organization or user instead of a directory: any
gh or Git HTTPS access to that owner's repositories uses this account, and
outranks the directory pin.

Examples:
  gh autoaccount pin alice
  gh autoaccount pin bob-work --dir ~/work --git-email bob@example.test
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
	// Retired; kept so a script that passes it gets the replacement, not "unknown flag".
	cmd.Flags().BoolVar(&exportToken, "export-token", false, "")
	_ = cmd.Flags().MarkHidden("export-token")

	return cmd
}

func runPin(user, dir, gitEmail, gitName, sshKey string, exportToken bool) error {
	return runPinWithHost(user, dir, gitEmail, gitName, sshKey, "", "", exportToken)
}

func runPinWithHost(user, dir, gitEmail, gitName, sshKey, host, tokenCommand string, exportToken bool) error {
	if exportToken {
		return errors.New(legacyRemoved)
	}
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

	pin := config.Pin{
		User:         user,
		Host:         host,
		TokenCommand: tokenCommand,
		Dir:          absDir,
		Mode:         config.ModeWrapper,
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
	registry, err := config.LoadPins()
	if err != nil {
		return fmt.Errorf("cannot load pin registry: %w", err)
	}
	previous := &config.PinRegistry{Pins: append([]config.Pin(nil), registry.Pins...)}
	if err := registry.AddPinStrict(pin); err != nil {
		return err
	}
	if err := savePinsAndIdentity(registry); err != nil {
		return fmt.Errorf("cannot save pin and Git identity: %w", err)
	}
	if _, err := refreshGitHelpers(registry, previous); err != nil {
		return err
	}

	fmt.Printf("\nPinned '%s' -> %s\n", user, absDir)
	if gitEmail != "" {
		fmt.Printf("  Git email:  %s\n", gitEmail)
	}
	if gitName != "" {
		fmt.Printf("  Git name:   %s\n", gitName)
	}
	if sshKey != "" {
		fmt.Printf("  SSH key:    %s\n", sshKey)
	}
	fmt.Println("\n  Token is injected per-command (never in shell environment).")
	if legacy.HasManagedEnvrc(absDir) {
		fmt.Println("  This directory's .envrc still has a retired gh-autoprofile block; run: gh autoaccount setup --migrate")
	}
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
func refreshGitHelpers(registry, previous *config.PinRegistry) (gitcred.HelperSnapshot, error) {
	if !gitcred.CheckConfigured() {
		return nil, nil
	}
	var helpers gitcred.HelperSnapshot
	var pathHosts []string
	hosts, err := gitcred.Hosts(registry)
	if err == nil {
		pathHosts, err = gitcred.OwnerHosts(registry)
	}
	if err == nil {
		helpers, err = gitcred.SnapshotHosts(hosts)
	}
	if err == nil {
		err = gitcred.ConfigureHosts(hosts, pathHosts...)
	}
	if err != nil {
		return helpers, errors.Join(fmt.Errorf("cannot refresh Git helpers: %w", err), gitcred.RestoreHosts(helpers), savePinsAndIdentity(previous))
	}
	return helpers, nil
}

// runPinOwner pins an account to a repository owner. Owner pins never touch
// .envrc or direnv: they are selected from the registry per command.
func runPinOwner(user, owner, gitEmail, gitName, sshKey, host, tokenCommand string, exportToken bool) error {
	if exportToken {
		return errors.New(legacyRemoved)
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
	if _, err := refreshGitHelpers(registry, previous); err != nil {
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
		fmt.Println("  Plain git HTTPS needs the credential helper: gh autoaccount setup")
	}
	return nil
}
