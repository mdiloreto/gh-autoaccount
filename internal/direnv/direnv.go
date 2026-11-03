package direnv

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
)

//go:embed shell/gh-autoprofile.sh
var shellLibContent []byte

//go:embed shell/gh-autoprofile-hook.sh
var shellHookContent []byte

const (
	markerStart = "# gh-autoprofile:start"
	markerEnd   = "# gh-autoprofile:end"

	hookMarkerStart = "# gh-autoprofile-hook:start"
	hookMarkerEnd   = "# gh-autoprofile-hook:end"
)

// IsInstalled checks if direnv is available in PATH.
func IsInstalled() bool {
	_, err := exec.LookPath("direnv")
	return err == nil
}

// GetVersion returns the direnv version string.
func GetVersion() (string, error) {
	cmd := exec.Command("direnv", "version")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ShellLibDir returns the path to direnv's lib directory where custom
// shell functions are auto-loaded from.
//
// Resolution order mirrors direnv's config loading:
//  1. DIRENV_CONFIG - explicit config root override
//  2. XDG_CONFIG_HOME/direnv - standard XDG location
//  3. ~/.config/direnv - default fallback
//
// direnv auto-loads third-party use_* functions from the lib/ directory under
// its active config root. If that root is overridden, the shell library must
// be installed there too.
func ShellLibDir() (string, error) {
	// DIRENV_CONFIG takes highest priority because direnv uses it as the
	// active config root.
	if direnvConfig := os.Getenv("DIRENV_CONFIG"); direnvConfig != "" {
		return filepath.Join(direnvConfig, "lib"), nil
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "direnv", "lib"), nil
}

// ShellLibPath returns the full path to the installed shell library.
func ShellLibPath() (string, error) {
	dir, err := ShellLibDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gh-autoprofile.sh"), nil
}

// ShellHookPath returns the path to the installed shell hook script.
func ShellHookPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hook.sh"), nil
}

// InstallShellLib writes the embedded shell library to direnv's lib directory.
func InstallShellLib(extensionPath, realGHPath string) error {
	for _, path := range []string{extensionPath, realGHPath} {
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\x00") {
			return fmt.Errorf("shell library target must be an absolute executable path")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("shell library target is not executable: %s", path)
		}
	}
	content := renderShellLib(extensionPath, realGHPath)
	libDir, err := ShellLibDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(libDir, 0755); err != nil {
		return fmt.Errorf("cannot create direnv lib directory %s: %w", libDir, err)
	}

	dest := filepath.Join(libDir, "gh-autoprofile.sh")
	return config.WriteAtomicMode(dest, []byte(content), 0644)
}

func renderShellLib(extensionPath, realGHPath string) string {
	return strings.NewReplacer(
		"@GH_AUTOPROFILE_EXTENSION@", shellQuote(extensionPath),
		"@GH_AUTOPROFILE_REAL_GH@", shellQuote(realGHPath),
	).Replace(string(shellLibContent))
}

// InstallShellHook writes the shell hook script to the config directory.
// RC integration is handled separately. Legacy hooks create gh()/git() wrappers;
// native hooks remove stale wrappers and leave prompt routing as a no-op.
func InstallShellHook() (hookPath string, err error) {
	settings, err := config.LoadSettings()
	if err != nil {
		return "", fmt.Errorf("cannot load setup settings: %w", err)
	}
	// Write hook script to config dir.
	hookPath, err = ShellHookPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(hookPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("cannot create config directory %s: %w", dir, err)
	}
	content := string(shellHookContent)
	if settings.Native {
		content = strings.Replace(content, "_gh_autoprofile_installed_native=0", "_gh_autoprofile_installed_native=1", 1)
	}
	if err := config.WriteAtomicMode(hookPath, []byte(content), 0644); err != nil {
		return "", fmt.Errorf("cannot write hook script: %w", err)
	}
	return hookPath, nil
}

// InjectHookSource adds a `source <hookPath>` line into the given shell RC
// file, wrapped in markers so it can be updated/removed later.
func InjectHookSource(rcPath, hookPath string) error {
	block := hookMarkerStart + "\n" +
		"source " + shellQuote(hookPath) + "\n" +
		hookMarkerEnd + "\n"

	existing, err := os.ReadFile(rcPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot read %s: %w", rcPath, err)
	}

	content := string(existing)

	// Already present — replace the existing block.
	if strings.Contains(content, hookMarkerStart) {
		startIdx := strings.Index(content, hookMarkerStart)
		endIdx := strings.Index(content, hookMarkerEnd)
		if endIdx != -1 {
			endIdx += len(hookMarkerEnd)
			if endIdx < len(content) && content[endIdx] == '\n' {
				endIdx++
			}
			content = content[:startIdx] + block + content[endIdx:]
			return config.WriteAtomicPreserveMode(rcPath, []byte(content), 0644)
		}
	}

	// Append.
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + block
	return config.WriteAtomicPreserveMode(rcPath, []byte(content), 0644)
}

// CheckShellHookInstalled checks if the gh-autoprofile hook source line
// is present in common shell config files.
func CheckShellHookInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	files := []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), hookMarkerStart) {
			return true
		}
	}
	return false
}

// IsShellLibInstalled checks if the shell library file exists.
func IsShellLibInstalled() bool {
	path, err := ShellLibPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

// CheckDirenvHook looks for the direnv hook in common shell config files.
func CheckDirenvHook() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	files := []string{
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".config", "fish", "config.fish"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "direnv hook") || strings.Contains(string(data), "direnv.fish") {
			return true
		}
	}
	return false
}

// WriteEnvrc creates or updates the .envrc file in the pin's directory.
// Uses markers to manage only the gh-autoprofile block, preserving any
// existing user content in the .envrc.
//
// In wrapper mode (default) it writes: use_gh_autoprofile <user> ...
// In export mode it writes: use_gh_autoprofile_export <user> ...
func WriteEnvrc(pin config.Pin) error {
	_, err := WriteEnvrcSnapshot(pin)
	return err
}

// WriteEnvrcSnapshot returns the exact bytes generated for the atomic rewrite.
func WriteEnvrcSnapshot(pin config.Pin) ([]byte, error) {
	envrcPath := filepath.Join(pin.Dir, ".envrc")
	settings, err := config.LoadSettings()
	if err != nil {
		return nil, err
	}
	if settings.GitIdentity {
		// Let native Git precedence apply instead of exporting identity overrides.
		pin.GitEmail, pin.GitName = "", ""
	}

	// Choose the direnv function based on mode.
	fnName := "use_gh_autoprofile"
	if pin.EffectiveMode() == config.ModeExport {
		fnName = "use_gh_autoprofile_export"
	}

	// Build arguments.
	var args []string
	args = append(args, shellQuote(pin.User))
	if pin.GitEmail != "" || pin.GitName != "" || pin.SSHKey != "" {
		args = append(args, shellQuote(pin.GitEmail))
		if pin.GitName != "" || pin.SSHKey != "" {
			args = append(args, shellQuote(pin.GitName))
			if pin.SSHKey != "" {
				args = append(args, shellQuote(pin.SSHKey))
			}
		}
	}

	// Build the managed block.
	var block strings.Builder
	block.WriteString(markerStart + "\n")
	block.WriteString(fnName + " " + strings.Join(args, " ") + "\n")
	block.WriteString(markerEnd + "\n")

	// Read existing .envrc (if any).
	existing, err := os.ReadFile(envrcPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot read .envrc: %w", err)
	}

	var newContent string
	if len(existing) > 0 {
		content := string(existing)
		if strings.Contains(content, markerStart) {
			// Replace existing block.
			startIdx := strings.Index(content, markerStart)
			endIdx := strings.Index(content, markerEnd)
			if endIdx != -1 {
				endIdx += len(markerEnd)
				if endIdx < len(content) && content[endIdx] == '\n' {
					endIdx++
				}
				newContent = content[:startIdx] + block.String() + content[endIdx:]
			} else {
				// Malformed markers — append fresh block.
				newContent = content
				if !strings.HasSuffix(content, "\n") {
					newContent += "\n"
				}
				newContent += block.String()
			}
		} else {
			// Append block to existing content.
			newContent = content
			if !strings.HasSuffix(content, "\n") {
				newContent += "\n"
			}
			newContent += block.String()
		}
	} else {
		newContent = block.String()
	}

	data := []byte(newContent)
	if err := writeAndHardenPerms(envrcPath, data); err != nil {
		return nil, err
	}
	return data, nil
}

// HasUnmanagedEnvrcContent reports whether .envrc contains content outside the
// gh-autoprofile managed block. Migration must not approve that content on the
// user's behalf.
func HasUnmanagedEnvrcContent(dir string) (bool, error) {
	envrcPath := filepath.Join(dir, ".envrc")
	data, err := os.ReadFile(envrcPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot inspect .envrc: %w", err)
	}

	return HasUnmanagedContent(data), nil
}

func HasUnmanagedContent(data []byte) bool {
	content := string(data)
	start := strings.Index(content, markerStart)
	if start == -1 {
		return strings.TrimSpace(content) != ""
	}
	endOffset := strings.Index(content[start+len(markerStart):], markerEnd)
	if endOffset == -1 {
		return strings.TrimSpace(content) != ""
	}
	end := start + len(markerStart) + endOffset + len(markerEnd)
	outside := content[:start] + content[end:]
	return strings.TrimSpace(outside) != ""
}

// AutoAllowSafe checks local permission bits; callers check before and after rewrites.
// It is a shared-write mitigation, not an atomic content authorization guarantee.
func AutoAllowSafe(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Errorf("cannot inspect directory %s: %w", dir, err)
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return false, nil
	}
	envrcInfo, err := os.Lstat(filepath.Join(dir, ".envrc"))
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("cannot inspect .envrc: %w", err)
	}
	return envrcInfo.Mode().IsRegular() && envrcInfo.Mode().Perm()&0022 == 0, nil
}

// RemoveEnvrc removes the gh-autoprofile block from .envrc.
// If the file is empty after removal, it deletes the file entirely.
func RemoveEnvrc(dir string) error {
	envrcPath := filepath.Join(dir, ".envrc")

	existing, err := os.ReadFile(envrcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot read .envrc: %w", err)
	}

	content := string(existing)
	if !strings.Contains(content, markerStart) {
		return nil // No gh-autoprofile block
	}

	startIdx := strings.Index(content, markerStart)
	endIdx := strings.Index(content, markerEnd)
	if endIdx == -1 {
		return nil
	}
	endIdx += len(markerEnd)
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}

	newContent := strings.TrimSpace(content[:startIdx] + content[endIdx:])
	if newContent == "" {
		return os.Remove(envrcPath)
	}

	return writeAndHardenPerms(envrcPath, []byte(newContent+"\n"))
}

// RequireManualEnvrcCleanup refuses native operations until managed blocks are removed manually.
func RequireManualEnvrcCleanup(dir string) error {
	path := filepath.Join(dir, ".envrc")
	data, err := readEnvrcNoFollow(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot safely inspect .envrc %s: %w", path, err)
	}
	if bytes.Contains(data, []byte(markerStart)) || bytes.Contains(data, []byte(markerEnd)) {
		return fmt.Errorf("managed or malformed markers in %s: run direnv deny %s manually, then manually remove only the gh-autoprofile managed block, preserving all other content, before retrying", path, path)
	}
	return nil
}

// CleanupManagedEnvrc revokes authorization before removing the managed block.
func CleanupManagedEnvrc(dir string) (bool, error) {
	path := filepath.Join(dir, ".envrc")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refusing non-regular .envrc: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	content := string(data)
	start, end := -1, -1
	for pos := 0; pos < len(content); {
		next := strings.IndexByte(content[pos:], '\n')
		lineEnd := len(content)
		if next >= 0 {
			lineEnd = pos + next + 1
		}
		line := strings.TrimSuffix(strings.TrimSuffix(content[pos:lineEnd], "\n"), "\r")
		if strings.Contains(line, markerStart) || strings.Contains(line, markerEnd) {
			switch line {
			case markerStart:
				if start != -1 || end != -1 {
					return false, fmt.Errorf("malformed .envrc markers: %s", path)
				}
				start = pos
			case markerEnd:
				if start == -1 || end != -1 {
					return false, fmt.Errorf("malformed .envrc markers: %s", path)
				}
				end = lineEnd
			default:
				return false, fmt.Errorf("malformed .envrc markers: %s", path)
			}
		}
		pos = lineEnd
	}
	if start == -1 && end == -1 {
		return false, nil
	}
	if start == -1 || end == -1 {
		return false, fmt.Errorf("malformed .envrc markers: %s", path)
	}
	if !IsInstalled() {
		return false, fmt.Errorf("direnv required to revoke authorization for %s", path)
	}
	cmd := exec.Command("direnv", "deny", path)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("direnv deny failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || !currentInfo.Mode().IsRegular() || !os.SameFile(info, currentInfo) {
		return false, fmt.Errorf("authorization revoked but .envrc changed during cleanup; review it and run direnv allow manually if appropriate")
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, current) {
		return false, fmt.Errorf("authorization revoked but .envrc changed during cleanup; review it and run direnv allow manually if appropriate")
	}
	remaining := append([]byte(nil), data[:start]...)
	remaining = append(remaining, data[end:]...)
	if len(remaining) == 0 {
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("authorization revoked but .envrc removal failed: %w; review .envrc and run direnv allow manually if appropriate", err)
		}
	} else if err := writeAndHardenPerms(path, remaining); err != nil {
		return false, fmt.Errorf("authorization revoked but .envrc rewrite failed: %w; review .envrc and run direnv allow manually if appropriate", err)
	}
	return true, nil
}

// writeAndHardenPerms atomically writes data with 0600 permissions. It rejects
// an existing symlink; a link swapped in after inspection is replaced by the
// final rename rather than followed.
func writeAndHardenPerms(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write to symlink: %s", path)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return config.WriteAtomicMode(path, data, 0600)
}

// AllowEnvrc runs `direnv allow` on the .envrc file.
func AllowEnvrc(dir string) error {
	envrcPath := filepath.Join(dir, ".envrc")
	cmd := exec.Command("direnv", "allow", envrcPath)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("direnv allow failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// DenyEnvrc runs `direnv deny` on the .envrc file to revoke the
// allowlist entry. This is the counterpart to AllowEnvrc and should
// be called when unpinning a directory.
// Returns nil if direnv is not installed or the .envrc is already absent.
func DenyEnvrc(dir string) error {
	if !IsInstalled() {
		return nil
	}
	envrcPath := filepath.Join(dir, ".envrc")
	if _, err := os.Stat(envrcPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot inspect .envrc for direnv deny: %w", err)
	}
	cmd := exec.Command("direnv", "deny", envrcPath)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("direnv deny failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// shellQuote wraps a string in single quotes for safe shell interpolation.
// Single quotes inside the string are escaped as '\”.
func shellQuote(s string) string {
	// If the string is simple (alphanumeric, dash, dot, underscore, slash,
	// at, plus, colon) it doesn't need quoting.
	safe := true
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '/' || c == '@' || c == '+' || c == ':') {
			safe = false
			break
		}
	}
	if safe && len(s) > 0 {
		return s
	}

	// Escape using single quotes: replace ' with '\''
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
