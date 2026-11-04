package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

type Settings struct {
	ExternalShell bool   `yaml:"external_shell,omitempty"`
	GitIdentity   bool   `yaml:"git_identity,omitempty"`
	Native        bool   `yaml:"native,omitempty"`
	Mise          bool   `yaml:"mise,omitempty"`
	DefaultUser   string `yaml:"default_user,omitempty"`
}

func LoadSettings() (Settings, error) {
	var settings Settings
	dir, err := ConfigDir()
	if err != nil {
		return settings, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "settings.yml"))
	if os.IsNotExist(err) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	// Unknown keys are rejected for the same fail-closed reason as pins.yml:
	// a typo would silently drop a setting (e.g. git_identity) and degrade
	// behavior without any warning.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&settings); err != nil {
		if errors.Is(err, io.EOF) {
			// Empty or comment-only settings file.
			return Settings{}, nil
		}
		return Settings{}, err
	}
	if strings.IndexFunc(settings.DefaultUser, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return Settings{}, fmt.Errorf("invalid default_user")
	}
	return settings, nil
}

func SaveSettings(settings Settings) error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(settings)
	if err != nil {
		return err
	}
	return WriteAtomic(filepath.Join(dir, "settings.yml"), data)
}
