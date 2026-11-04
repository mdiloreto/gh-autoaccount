package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PinMode controls how tokens are injected into the shell.
type PinMode string

const (
	// ModeWrapper (default) — direnv exports only the GH_AUTOPROFILE_USER
	// marker; a shell hook creates gh()/git() wrapper functions that inject
	// the token per-invocation (~30 ms). The token never sits in the
	// shell environment.
	ModeWrapper PinMode = "wrapper"

	// ModeExport — direnv exports GH_TOKEN and GITHUB_TOKEN directly into
	// the shell environment (legacy behaviour). Use for directories where
	// third-party tools need the env var.
	ModeExport PinMode = "export"
)

// Pin maps either a directory or a repository owner to an account. Exactly one
// of Dir and Owner is set; owner pins are wrapper-mode only and have no .envrc.
type Pin struct {
	User         string  `yaml:"user"`
	Host         string  `yaml:"host,omitempty"`
	TokenCommand string  `yaml:"token_command,omitempty"`
	Dir          string  `yaml:"dir,omitempty"`
	Owner        string  `yaml:"owner,omitempty"`
	Mode         PinMode `yaml:"mode,omitempty"`
	GitEmail     string  `yaml:"git_email,omitempty"`
	GitName      string  `yaml:"git_name,omitempty"`
	SSHKey       string  `yaml:"ssh_key,omitempty"`
}

// ValidLogin reports whether s is a syntactically valid GitHub login or
// organization name, including Enterprise Managed User shortcodes.
func ValidLogin(s string) bool {
	if len(s) == 0 || len(s) > 39 {
		return false
	}
	base, shortcode, managed := strings.Cut(s, "_")
	if base == "" || base[0] == '-' || base[len(base)-1] == '-' || strings.Contains(base, "--") || managed && shortcode == "" {
		return false
	}
	for _, c := range base {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	for _, c := range shortcode {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// NormalizeHost validates a hostname and applies the legacy github.com default.
func NormalizeHost(host string) (string, error) {
	if host == "" {
		return "github.com", nil
	}
	for _, c := range host {
		if c > 127 {
			return "", fmt.Errorf("invalid hostname")
		}
	}
	host = strings.ToLower(host)
	if len(host) > 253 {
		return "", fmt.Errorf("invalid hostname")
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("invalid hostname")
			}
		}
	}
	return host, nil
}

// CanonicalDir resolves an existing directory to its physical path.
func CanonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", path)
	}
	return resolved, nil
}

// FindPinStrict resolves physical ancestry and rejects ambiguous or unreadable pins.
func (r *PinRegistry) FindPinStrict(dir string) (*Pin, error) {
	return r.findPinStrict(dir, CanonicalDir)
}

func (r *PinRegistry) findPinStrict(dir string, canonicalDir func(string) (string, error)) (*Pin, error) {
	lexical, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	physical, err := canonicalDir(dir)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var best *Pin
	bestLen := -1
	for i := range r.Pins {
		p := &r.Pins[i]
		if p.IsOwner() {
			continue
		}
		path, err := canonicalDir(p.Dir)
		if err != nil {
			stored, absErr := filepath.Abs(p.Dir)
			if absErr != nil || pathContains(stored, lexical) || pathContains(stored, physical) {
				return nil, fmt.Errorf("cannot resolve pin directory %q: %w", p.Dir, err)
			}
			continue
		}
		if seen[path] {
			return nil, fmt.Errorf("duplicate canonical pin directory %q", path)
		}
		seen[path] = true
		rel, err := filepath.Rel(path, physical)
		if err != nil {
			return nil, err
		}
		if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) && len(path) > bestLen {
			best, bestLen = p, len(path)
		}
	}
	return best, nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// IsOwner reports whether the pin selects by repository owner, not directory.
func (p *Pin) IsOwner() bool { return p.Owner != "" }

// EffectiveMode returns the pin's mode, defaulting to ModeWrapper.
func (p *Pin) EffectiveMode() PinMode {
	if p.Mode == "" {
		return ModeWrapper
	}
	return p.Mode
}

// PinRegistry holds all directory pins.
type PinRegistry struct {
	Pins []Pin `yaml:"pins"`
}

// ConfigDir returns the gh-autoprofile config directory path.
// Respects XDG_CONFIG_HOME, defaults to ~/.config/gh-autoprofile.
func ConfigDir() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "gh-autoprofile"), nil
}

// PinsFilePath returns the full path to the pins.yml file.
func PinsFilePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pins.yml"), nil
}

// LoadPins reads the pin registry from disk.
// Returns an empty registry if the file doesn't exist.
//
// Unknown fields are rejected: pins.yml is only written by this tool, so an
// unrecognized key means a hand-edit typo (e.g. `ssh_keyy:`) that would
// otherwise silently drop account scoping and fail open to the default
// account. Failing loudly with the offending key keeps that failure closed.
func LoadPins() (*PinRegistry, error) {
	path, err := PinsFilePath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PinRegistry{}, nil
		}
		return nil, fmt.Errorf("cannot read pins file: %w", err)
	}

	var registry PinRegistry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&registry); err != nil {
		if errors.Is(err, io.EOF) {
			// Empty or comment-only registry file.
			return &PinRegistry{}, nil
		}
		return nil, fmt.Errorf("cannot parse pins file %s (unknown keys are rejected; check for typos in hand-edited fields): %w", path, err)
	}
	if err := registry.validate(); err != nil {
		return nil, fmt.Errorf("invalid pins file %s: %w", path, err)
	}
	return &registry, nil
}

// SavePins writes the pin registry to disk, creating directories as needed.
func SavePins(registry *PinRegistry) error {
	path, err := PinsFilePath()
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("cannot create config directory %s: %w", dir, err)
	}

	data, err := yaml.Marshal(registry)
	if err != nil {
		return fmt.Errorf("cannot marshal pins: %w", err)
	}

	return WriteAtomic(path, data)
}

// FindPin returns the closest matching pin for a directory or one of its parents.
func (r *PinRegistry) FindPin(dir string) *Pin {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	var best *Pin
	for i := range r.Pins {
		if r.Pins[i].IsOwner() {
			continue
		}
		pinnedDir := r.Pins[i].Dir
		if absDir == pinnedDir || strings.HasPrefix(absDir, pinnedDir+string(os.PathSeparator)) {
			if best == nil || len(pinnedDir) > len(best.Dir) {
				best = &r.Pins[i]
			}
		}
	}
	return best
}

// FindPinForDir returns the closest matching pin for dir.
func (r *PinRegistry) FindPinForDir(dir string) *Pin {
	return r.FindPin(dir)
}

// FindPinByPath returns the pin that matches the given directory or its
// nearest ancestor. When multiple pins match (nested directories), the
// deepest (most-specific) pin wins.
//
// For example, if pins exist for /home/user/work and /home/user/work/client-a,
// a query for /home/user/work/client-a/repo returns the /home/user/work/client-a pin.
//
// Returns nil if no pin matches.
func (r *PinRegistry) FindPinByPath(dir string) *Pin {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}

	// First try exact match (fast path).
	for i := range r.Pins {
		if r.Pins[i].Dir == absDir {
			return &r.Pins[i]
		}
	}

	// Walk up the directory tree, collecting the deepest matching pin.
	var best *Pin
	bestLen := 0
	for i := range r.Pins {
		pinDir := r.Pins[i].Dir
		// Check if absDir is under pinDir: pinDir must be a prefix and
		// the next character must be a path separator (or absDir == pinDir,
		// already handled above).
		if len(pinDir) > bestLen && len(pinDir) < len(absDir) &&
			absDir[:len(pinDir)] == pinDir && absDir[len(pinDir)] == filepath.Separator {
			best = &r.Pins[i]
			bestLen = len(pinDir)
		}
	}
	return best
}

// AddPinStrict validates and stores a physical pin without changing legacy entries.
func (r *PinRegistry) AddPinStrict(pin Pin) error {
	dir, err := CanonicalDir(pin.Dir)
	if err != nil {
		return err
	}
	host, err := NormalizeHost(pin.Host)
	if err != nil {
		return err
	}
	pin.Dir = dir
	if pin.Host != "" {
		pin.Host = host
	}
	match := -1
	for i := range r.Pins {
		if r.Pins[i].IsOwner() {
			continue
		}
		stored, err := CanonicalDir(r.Pins[i].Dir)
		if err == nil && stored == dir {
			if match != -1 {
				return fmt.Errorf("duplicate canonical pin directory %q", dir)
			}
			match = i
		}
	}
	if match != -1 {
		r.Pins[match] = pin
		return nil
	}
	r.Pins = append(r.Pins, pin)
	return nil
}

// AddPin adds or updates a pin in the registry.
func (r *PinRegistry) AddPin(pin Pin) {
	absDir, err := CanonicalDir(pin.Dir)
	if err != nil {
		absDir, err = filepath.Abs(pin.Dir)
	}
	if err == nil {
		pin.Dir = absDir
	}
	if pin.Host != "" {
		if host, err := NormalizeHost(pin.Host); err == nil {
			pin.Host = host
		}
	}

	// Update existing pin for same directory
	for i := range r.Pins {
		if r.Pins[i].Dir == pin.Dir {
			r.Pins[i] = pin
			return
		}
	}
	r.Pins = append(r.Pins, pin)
}

// RemovePin removes a pin by directory path. Returns true if found and removed.
func (r *PinRegistry) RemovePin(dir string) bool {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for i := range r.Pins {
		if r.Pins[i].IsOwner() {
			continue
		}
		stored, storedErr := filepath.Abs(r.Pins[i].Dir)
		if r.Pins[i].Dir == dir || storedErr == nil && stored == absDir {
			r.Pins = append(r.Pins[:i], r.Pins[i+1:]...)
			return true
		}
	}
	canonical, err := CanonicalDir(dir)
	if err != nil {
		return false
	}
	for i := range r.Pins {
		if r.Pins[i].IsOwner() {
			continue
		}
		resolved, err := CanonicalDir(r.Pins[i].Dir)
		if err == nil && canonical == resolved {
			r.Pins = append(r.Pins[:i], r.Pins[i+1:]...)
			return true
		}
	}
	return false
}

// validate enforces the dir-xor-owner shape and owner uniqueness. Legacy
// directory-only registries always pass.
func (r *PinRegistry) validate() error {
	seen := make(map[string]bool)
	for i := range r.Pins {
		p := &r.Pins[i]
		if (p.Dir == "") == (p.Owner == "") {
			return fmt.Errorf("pin %d (%q) must set exactly one of dir or owner", i+1, p.User)
		}
		if !p.IsOwner() {
			continue
		}
		key, err := p.ownerKey()
		if err != nil {
			return fmt.Errorf("pin %d (%q): %w", i+1, p.User, err)
		}
		if seen[key] {
			return fmt.Errorf("duplicate owner pin %q for host %q", p.Owner, p.Host)
		}
		seen[key] = true
	}
	return nil
}

// ownerKey validates an owner pin and returns its (host, owner) identity.
func (p *Pin) ownerKey() (string, error) {
	if p.Dir != "" {
		return "", fmt.Errorf("owner pins must not set dir")
	}
	if !ValidLogin(p.Owner) {
		return "", fmt.Errorf("invalid owner %q", p.Owner)
	}
	if p.EffectiveMode() != ModeWrapper {
		return "", fmt.Errorf("owner pins support wrapper mode only")
	}
	host, err := NormalizeHost(p.Host)
	if err != nil {
		return "", err
	}
	return host + "/" + strings.ToLower(p.Owner), nil
}

// HasOwnerPins reports whether any pin selects by owner.
func (r *PinRegistry) HasOwnerPins() bool {
	for i := range r.Pins {
		if r.Pins[i].IsOwner() {
			return true
		}
	}
	return false
}

// FindOwnerPin returns the owner pin for host and owner (case-insensitive).
func (r *PinRegistry) FindOwnerPin(host, owner string) *Pin {
	host, err := NormalizeHost(host)
	if err != nil || owner == "" {
		return nil
	}
	for i := range r.Pins {
		p := &r.Pins[i]
		if !p.IsOwner() {
			continue
		}
		if pinHost, err := NormalizeHost(p.Host); err == nil && pinHost == host && strings.EqualFold(p.Owner, owner) {
			return p
		}
	}
	return nil
}

// AddOwnerPin validates and stores an owner pin, replacing one for the same
// (host, owner).
func (r *PinRegistry) AddOwnerPin(pin Pin) error {
	key, err := pin.ownerKey()
	if err != nil {
		return err
	}
	if pin.Host != "" {
		pin.Host, _ = NormalizeHost(pin.Host)
	}
	for i := range r.Pins {
		if existing := &r.Pins[i]; existing.IsOwner() {
			if existingKey, err := existing.ownerKey(); err == nil && existingKey == key {
				r.Pins[i] = pin
				return nil
			}
		}
	}
	r.Pins = append(r.Pins, pin)
	return nil
}

// RemoveOwnerPin removes the owner pin for host and owner. Returns true if found.
func (r *PinRegistry) RemoveOwnerPin(host, owner string) bool {
	pin := r.FindOwnerPin(host, owner)
	if pin == nil {
		return false
	}
	for i := range r.Pins {
		if &r.Pins[i] == pin {
			r.Pins = append(r.Pins[:i], r.Pins[i+1:]...)
			return true
		}
	}
	return false
}
