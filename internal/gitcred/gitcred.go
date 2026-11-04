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

var helperTargetPattern = regexp.MustCompile(`exec '((?:[^']|'\\'')*)' __git-credential "\$@"\n\z`)

// Target returns the executable the managed helper script delegates to.
func Target() (string, error) {
	target, found, err := managedHelperValue(helperTargetPattern)
	if err == nil && !found {
		err = fmt.Errorf("git credential helper target not found")
	}
	return target, err
}

var helperRealGHPattern = regexp.MustCompile(`GH_AUTOPROFILE_REAL_GH='((?:[^']|'\\'')*)' exec `)

// RealGH returns the real gh path the managed helper pins, or "" when the
// helper predates pinning and resolves gh at runtime.
func RealGH() (string, error) {
	realGH, _, err := managedHelperValue(helperRealGHPattern)
	return realGH, err
}

// managedHelperValue returns the unquoted value pattern captures from the
// managed helper script.
func managedHelperValue(pattern *regexp.Regexp) (string, bool, error) {
	helperPath, err := HelperPath()
	if err != nil {
		return "", false, err
	}
	content, err := os.ReadFile(helperPath)
	if err != nil {
		return "", false, err
	}
	if !managedHelperPattern.Match(content) {
		return "", false, fmt.Errorf("git credential helper is not managed")
	}
	match := pattern.FindSubmatch(content)
	if match == nil {
		return "", false, nil
	}
	return strings.ReplaceAll(string(match[1]), `'\''`, "'"), true, nil
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

// OwnerHosts returns the normalized hosts that have an owner pin, the only
// hosts where useHttpPath is needed.
func OwnerHosts(registry *config.PinRegistry) ([]string, error) {
	var hosts []string
	for _, pin := range registry.Pins {
		if !pin.IsOwner() {
			continue
		}
		host, err := config.NormalizeHost(pin.Host)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// HelperSnapshot records, per host, every global value this package manages in
// the host's credential section, so rollback restores exactly what existed.
type HelperSnapshot map[string]HostState

// HostState is the managed global configuration of one host section.
type HostState struct{ Helpers, UseHTTPPath []string }

func SnapshotHosts(hosts []string) (HelperSnapshot, error) {
	snapshot := HelperSnapshot{}
	for _, host := range hosts {
		normalized, err := config.NormalizeHost(host)
		if err != nil {
			return nil, err
		}
		var prior HostState
		if prior.Helpers, err = configuredHelpersHost(normalized); err != nil {
			return nil, err
		}
		if prior.UseHTTPPath, err = globalValues(useHTTPPathKey(normalized)); err != nil {
			return nil, err
		}
		snapshot[normalized] = prior
	}
	return snapshot, nil
}

func RestoreHosts(snapshot HelperSnapshot) error {
	var result error
	for host, prior := range snapshot {
		result = errors.Join(result, replaceHelpersHost(host, prior.Helpers), replaceGlobalValues(useHTTPPathKey(host), prior.UseHTTPPath))
	}
	return result
}

// ConfigureHosts installs the helper chain on hosts and enables useHttpPath on
// pathHosts only. useHttpPath is never unset automatically: other helpers may
// rely on it.
func ConfigureHosts(hosts []string, pathHosts ...string) error {
	snapshot, err := SnapshotHosts(hosts)
	if err != nil {
		return err
	}
	wantPath := map[string]bool{}
	for _, host := range pathHosts {
		normalized, err := config.NormalizeHost(host)
		if err != nil {
			return err
		}
		wantPath[normalized] = true
	}
	for host, existing := range snapshot {
		if err := configureHost(host, existing.Helpers, existing.UseHTTPPath, wantPath[host]); err != nil {
			return errors.Join(err, RestoreHosts(snapshot))
		}
	}
	return nil
}

// configureHost installs the helper chain and, for hosts with owner pins,
// enables useHttpPath: Git sends the repository path to helpers only then.
func configureHost(host string, existing, useHTTPPath []string, enablePath bool) error {
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
	if err := replaceHelpersHost(host, desired); err != nil {
		return err
	}
	if !enablePath || len(useHTTPPath) == 1 && useHTTPPath[0] == "true" {
		return nil
	}
	if value, isFalse := explicitFalse(useHTTPPath); isFalse {
		fmt.Fprintf(os.Stderr, "Note: overwriting credential.https://%s.useHttpPath=%s with true; owner pins need request paths.\n", host, value)
	}
	return replaceGlobalValues(useHTTPPathKey(host), []string{"true"})
}

func configuredHelpers() ([]string, error) {
	return configuredHelpersHost("github.com")
}

func configuredHelpersHost(host string) ([]string, error) {
	return globalValues("credential.https://" + host + ".helper")
}

// explicitFalse reports whether the effective (last) value is a Git false.
func explicitFalse(values []string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	last := strings.ToLower(strings.TrimSpace(values[len(values)-1]))
	switch last {
	case "false", "no", "off", "0":
		return last, true
	}
	return "", false
}

func useHTTPPathKey(host string) string { return "credential.https://" + host + ".useHttpPath" }

// globalValues returns every global value of key, or nil when it is unset.
func globalValues(key string) ([]string, error) {
	cmd := exec.Command("git", "config", "--global", "--get-all", key)
	cmd.Env = append([]string{}, os.Environ()...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("cannot read existing Git configuration %s: %w", key, err)
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
	return replaceGlobalValues("credential.https://"+host+".helper", helpers)
}

// replaceGlobalValues sets key to exactly values; none unsets it.
func replaceGlobalValues(key string, helpers []string) error {
	unset := exec.Command("git", "config", "--global", "--unset-all", key)
	unset.Env = append([]string{}, os.Environ()...)
	if err := unset.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 5 {
			return fmt.Errorf("cannot clear Git configuration %s: %w", key, err)
		}
	}
	for _, helper := range helpers {
		cmd := exec.Command("git", "config", "--global", "--add", key, helper)
		cmd.Env = append([]string{}, os.Environ()...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("cannot add Git configuration %s=%q: %w", key, helper, err)
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

// CheckUseHTTPPathHost reports whether Git passes request paths to helpers for host.
func CheckUseHTTPPathHost(host string) bool {
	host, err := config.NormalizeHost(host)
	if err != nil {
		return false
	}
	out, err := exec.Command("git", "config", "--global", "--type=bool", "--get", useHTTPPathKey(host)).Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
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
