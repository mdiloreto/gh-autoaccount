package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// NormalizeEnv validates the environment roots that gh and Git derive default
// paths from, fails closed when no absolute home is available, and removes
// relative overrides that would otherwise resolve inside the working directory.
func NormalizeEnv() error {
	if err := normalizeHome(); err != nil {
		return err
	}
	names := []string{
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
		"GH_CONFIG_DIR",
		"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM",
	}
	if runtime.GOOS == "windows" {
		names = append(names, "AppData", "LocalAppData")
	}
	for _, name := range names {
		if value, set := os.LookupEnv(name); set && !filepath.IsAbs(value) {
			if err := os.Unsetenv(name); err != nil {
				return fmt.Errorf("cannot unset %s: %w", name, err)
			}
		}
	}
	return nil
}

func normalizeHome() error {
	home, homeSet := os.LookupEnv("HOME")
	homeValid := homeSet && home != "" && filepath.IsAbs(home)
	if runtime.GOOS != "windows" {
		if !homeSet || home == "" {
			return fmt.Errorf("HOME is not set; set it to an absolute path")
		}
		if !homeValid {
			return fmt.Errorf("HOME must be an absolute path: %q", home)
		}
		return nil
	}
	profile, profileSet := os.LookupEnv("USERPROFILE")
	profileValid := profileSet && profile != "" && filepath.IsAbs(profile)
	switch {
	case !homeValid && !profileValid:
		return fmt.Errorf("HOME and USERPROFILE are not set to absolute paths")
	case !homeValid:
		if err := os.Setenv("HOME", profile); err != nil {
			return fmt.Errorf("cannot set HOME from USERPROFILE: %w", err)
		}
	case !profileValid:
		if err := os.Setenv("USERPROFILE", home); err != nil {
			return fmt.Errorf("cannot set USERPROFILE from HOME: %w", err)
		}
	}
	return nil
}

// HomeDir returns the home directory and rejects a relative HOME.
func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("home directory must be an absolute path: %q", home)
	}
	return home, nil
}

// ConfigHome returns the XDG config home, ignoring relative XDG_CONFIG_HOME values.
func ConfigHome() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

// DataHome returns the XDG data home, ignoring relative XDG_DATA_HOME values.
func DataHome() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return dir, nil
	}
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}
