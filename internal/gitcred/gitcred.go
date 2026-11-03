package gitcred

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

const (
	helperFallback = "!gh auth git-credential"
	helperSection  = "credential.https://github.com"
)

// HelperPath returns the installed git helper script path.
func HelperPath() (string, error) {
	configDir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "git-credential-helper"), nil
}

var managedHelperPattern = regexp.MustCompile(`\A(?:#!/usr/bin/env bash\nset -euo pipefail\nexec '(?:[^']|'\\'')*' __git-credential "\$@"\n|#!/bin/sh\n# gh-autoprofile-git-credential-helper\n(?:GH_PATH='(?:[^']|'\\'')*' )?GH_AUTOPROFILE_REAL_GH='(?:[^']|'\\'')*' exec '(?:[^']|'\\'')*' __git-credential "\$@"\n)\z`)

// Install writes the managed helper script with a fixed real-gh target.
func Install(extensionBinaryPath, realGH string) (string, error) {
	if !filepath.IsAbs(extensionBinaryPath) || !filepath.IsAbs(realGH) {
		return "", fmt.Errorf("helper targets must be absolute paths")
	}
	helperPath, err := HelperPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(helperPath), 0700); err != nil {
		return "", fmt.Errorf("cannot create helper directory: %w", err)
	}
	info, err := os.Lstat(helperPath)
	if err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("refusing to overwrite unmanaged git credential helper")
		}
		prior, err := os.ReadFile(helperPath)
		if err != nil {
			return "", fmt.Errorf("cannot inspect git credential helper: %w", err)
		}
		if !managedHelperPattern.Match(prior) {
			return "", fmt.Errorf("refusing to overwrite unmanaged git credential helper")
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("cannot inspect git credential helper: %w", err)
	}
	content := fmt.Sprintf(`#!/bin/sh
# gh-autoprofile-git-credential-helper
GH_PATH=%s GH_AUTOPROFILE_REAL_GH=%s exec %s __git-credential "$@"
`, shellQuote(realGH), shellQuote(realGH), shellQuote(extensionBinaryPath))
	if err := config.WriteAtomicMode(helperPath, []byte(content), 0755); err != nil {
		return "", fmt.Errorf("cannot write git credential helper: %w", err)
	}
	return helperPath, nil
}

// IsInstalled reports whether the helper script exists.
func IsInstalled() bool {
	path, err := HelperPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// ConfigureGit installs the helper chain for github.com while preserving the
// existing gh fallback helper.
func ConfigureGit() error {
	return ConfigureHosts([]string{"github.com"})
}

// Hosts returns the exact host sections required for the registry.
func Hosts(registry *config.PinRegistry) ([]string, error) {
	hosts := map[string]bool{"github.com": true}
	for _, pin := range registry.Pins {
		host, err := config.NormalizeHost(pin.Host)
		if err != nil {
			return nil, err
		}
		hosts[host] = true
	}
	result := make([]string, 0, len(hosts))
	for host := range hosts {
		result = append(result, host)
	}
	sort.Strings(result)
	return result, nil
}

type HelperSnapshot map[string][]string

func SnapshotHosts(hosts []string) (HelperSnapshot, error) {
	snapshot := HelperSnapshot{}
	for _, host := range hosts {
		normalized, err := config.NormalizeHost(host)
		if err != nil {
			return nil, err
		}
		snapshot[normalized], err = configuredHelpersHost(normalized)
		if err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func RestoreHosts(snapshot HelperSnapshot) error {
	var result error
	for host, helpers := range snapshot {
		result = errors.Join(result, replaceHelpersHost(host, helpers))
	}
	return result
}

func ConfigureHosts(hosts []string) error {
	snapshot, err := SnapshotHosts(hosts)
	if err != nil {
		return err
	}
	for host, existing := range snapshot {
		if err := configureHost(host, existing); err != nil {
			return errors.Join(err, RestoreHosts(snapshot))
		}
	}
	return nil
}

func configureHost(host string, existing []string) error {
	helperPath, err := HelperPath()
	if err != nil {
		return err
	}
	primary := primaryHelper(helperPath)
	desired := []string{"", primary}
	for i := len(existing) - 1; i >= 0; i-- {
		if existing[i] == "" {
			existing = existing[i+1:]
			break
		}
	}
	for _, helper := range existing {
		if helper == "" || helper == helperFallback || isManagedHelper(helper, helperPath) {
			continue
		}
		desired = append(desired, helper)
	}
	desired = append(desired, helperFallback)
	return replaceHelpersHost(host, desired)
}

func configuredHelpers() ([]string, error) {
	return configuredHelpersHost("github.com")
}

func configuredHelpersHost(host string) ([]string, error) {
	cmd := exec.Command("git", "config", "--global", "--get-all", "credential.https://"+host+".helper")
	cmd.Env = append([]string{}, os.Environ()...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read existing Git helpers: %w", err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"), nil
}

// SnapshotHelpers returns the global GitHub helper chain for setup rollback.
func SnapshotHelpers() ([]string, error) { return configuredHelpers() }

// RestoreHelpers restores only the GitHub helper chain, not other Git settings.
func RestoreHelpers(helpers []string) error { return replaceConfiguredHelpers(helpers) }

func replaceConfiguredHelpers(helpers []string) error {
	return replaceHelpersHost("github.com", helpers)
}

func replaceHelpersHost(host string, helpers []string) error {
	key := "credential.https://" + host + ".helper"
	unset := exec.Command("git", "config", "--global", "--unset-all", key)
	unset.Env = append([]string{}, os.Environ()...)
	if err := unset.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 {
			return fmt.Errorf("cannot clear Git helper configuration: %w", err)
		}
	}
	for _, helper := range helpers {
		cmd := exec.Command("git", "config", "--global", "--add", key, helper)
		cmd.Env = append([]string{}, os.Environ()...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cannot add Git helper %q: %w", helper, err)
		}
	}
	return nil
}

func isManagedHelper(helper, path string) bool {
	return helper == primaryHelper(path) || helper == "!"+path+" get"
}

// CheckConfigured reports whether git is configured to use the managed helper.
func CheckConfigured() bool {
	return CheckConfiguredHost("github.com")
}

func CheckConfiguredHost(host string) bool {
	host, err := config.NormalizeHost(host)
	if err != nil {
		return false
	}
	helperPath, err := HelperPath()
	if err != nil {
		return false
	}
	cmd := exec.Command("git", "config", "--global", "--get-all", "credential.https://"+host+".helper")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		// Existing installations used an unquoted path with a fixed get action.
		if line == primaryHelper(helperPath) || line == "!"+helperPath+" get" {
			return true
		}
	}
	return false
}

func primaryHelper(path string) string {
	return "!" + shellQuote(path)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// HelperSummary returns the configured helper lines for github.com.
func HelperSummary() ([]string, error) {
	return HelperSummaryHost("github.com")
}

func HelperSummaryHost(host string) ([]string, error) {
	host, err := config.NormalizeHost(host)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("git", "config", "--global", "--get-all", "credential.https://"+host+".helper")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	return lines, nil
}
