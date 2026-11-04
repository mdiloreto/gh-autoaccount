package ghauth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
	"golang.org/x/term"
)

// UserInfo holds information about a logged-in GitHub account.
type UserInfo struct {
	User     string
	Host     string
	Active   bool
	Protocol string
	Valid    bool
	Scopes   []string
}

// TokenInfo describes the effective token source and granted scopes for a user.
type TokenInfo struct {
	User   string
	Source string
	Scopes []string
}

var scopeInspectionClient = &http.Client{Timeout: 10 * time.Second}

// IsInteractive reports whether stdin is a terminal (TTY).
// Non-interactive sessions (CI, cron, systemd, piped input) return false.
func IsInteractive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// GetToken retrieves the OAuth token for a specific gh user from the keyring
// without changing the active account.
func GetToken(user string) (string, error) {
	return ResolveToken(context.Background(), TokenRequest{User: user})
}

type TokenRequest struct{ User, Host, TokenCommand string }

const commandTimeout = 5 * time.Second
const maxCredentialOutput = 16384

var credentialVars = map[string]bool{"GH_TOKEN": true, "GITHUB_TOKEN": true, "GH_ENTERPRISE_TOKEN": true, "GITHUB_ENTERPRISE_TOKEN": true, "GH_HOST": true}

func cleanEnv(env []string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !credentialVars[key] {
			result = append(result, entry)
		}
	}
	return result
}

// CredentialEnv replaces every inherited GitHub credential and host variable.
func CredentialEnv(env []string, host, token string, fallback bool) ([]string, error) {
	host, err := config.NormalizeHost(host)
	if err != nil {
		return nil, err
	}
	if err := ValidateToken(token); err != nil {
		return nil, err
	}
	result := cleanEnv(env)
	primary, secondary := "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"
	if host == "github.com" || strings.HasSuffix(host, ".ghe.com") {
		primary, secondary = "GH_TOKEN", "GITHUB_TOKEN"
	}
	result = append(result, primary+"="+token)
	if fallback {
		result = append(result, secondary+"="+token)
	}
	if host != "github.com" {
		result = append(result, "GH_HOST="+host)
	}
	return result, nil
}

type limitedOutput struct {
	buffer   bytes.Buffer
	overflow bool
	cancel   context.CancelFunc
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > maxCredentialOutput-w.buffer.Len() {
		w.overflow = true
		w.cancel()
		return 0, fmt.Errorf("output too large")
	}
	return w.buffer.Write(p)
}

func runCredentialCommand(ctx context.Context, path string, args []string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	out := limitedOutput{cancel: cancel}
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil || out.overflow {
		return "", fmt.Errorf("credential command failed")
	}
	value := strings.TrimSuffix(out.buffer.String(), "\n")
	if err := ValidateToken(value); err != nil {
		return "", err
	}
	return value, nil
}

// ResolveToken retrieves the selected account's credential; a broker is authoritative.
func ResolveToken(ctx context.Context, request TokenRequest) (string, error) {
	host, err := config.NormalizeHost(request.Host)
	if err != nil {
		return "", err
	}
	if !validLogin(request.User) {
		return "", fmt.Errorf("invalid user")
	}
	if request.TokenCommand != "" {
		if !filepath.IsAbs(request.TokenCommand) {
			return "", fmt.Errorf("token command must be absolute")
		}
		path, err := filepath.EvalSymlinks(request.TokenCommand)
		if err != nil {
			return "", fmt.Errorf("invalid token command")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
			return "", fmt.Errorf("invalid token command")
		}
		env := cleanEnv(os.Environ())
		filtered := env[:0]
		for _, entry := range env {
			key, _, _ := strings.Cut(entry, "=")
			if key != "GH_AUTOPROFILE_USER" && key != "GH_AUTOPROFILE_HOST" {
				filtered = append(filtered, entry)
			}
		}
		env = append(filtered, "GH_AUTOPROFILE_USER="+request.User, "GH_AUTOPROFILE_HOST="+host)
		return runCredentialCommand(ctx, path, nil, env)
	}
	ghPath, err := ResolveGHPath()
	if err != nil {
		return "", err
	}
	return runCredentialCommand(ctx, ghPath, []string{"auth", "token", "--user", request.User, "--hostname", host}, cleanEnv(os.Environ()))
}

// ValidateToken rejects values that cannot safely be used as a credential field.
func ValidateToken(token string) error {
	if token == "" || len(token) > maxCredentialOutput {
		return fmt.Errorf("invalid token")
	}
	for _, r := range token {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == unicode.ReplacementChar {
			return fmt.Errorf("invalid token")
		}
	}
	return nil
}

// VerifyTokenIdentity checks the authenticated user using the real gh binary.
func VerifyTokenIdentity(ctx context.Context, user, host, token string) error {
	host, err := config.NormalizeHost(host)
	if err != nil {
		return err
	}
	if !validLogin(user) {
		return fmt.Errorf("invalid user")
	}
	path, err := ResolveGHPath()
	if err != nil {
		return err
	}
	env, err := CredentialEnv(os.Environ(), host, token, false)
	if err != nil {
		return err
	}
	filtered := env[:0]
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GH_DEBUG" && key != "DEBUG" && key != "GH_PROMPT_DISABLED" {
			filtered = append(filtered, entry)
		}
	}
	login, err := runCredentialCommand(ctx, path, []string{"api", "--hostname", host, "user", "--jq", ".login"}, append(filtered, "GH_PROMPT_DISABLED=1"))
	if err != nil || !validLogin(login) || !strings.EqualFold(login, user) {
		return fmt.Errorf("token identity verification failed")
	}
	return nil
}

func validLogin(user string) bool { return config.ValidLogin(user) }

// GetTokenWithFallbackForHost considers only the ambient pair for the selected host class.
func GetTokenWithFallbackForHost(ctx context.Context, request TokenRequest) (string, string, error) {
	host, err := config.NormalizeHost(request.Host)
	if err != nil {
		return "", "", err
	}
	if request.TokenCommand != "" {
		token, err := ResolveToken(ctx, request)
		return token, "token_command", err
	}
	keys := []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	if host == "github.com" || strings.HasSuffix(host, ".ghe.com") {
		keys = []string{"GH_TOKEN", "GITHUB_TOKEN"}
	}
	for _, key := range keys {
		if token := os.Getenv(key); token != "" {
			return token, key, ValidateToken(token)
		}
	}
	token, err := ResolveToken(ctx, request)
	if err != nil {
		return "", "", err
	}
	return token, "keyring", nil
}

// GetTokenWithFallback resolves a token for the given user, checking
// environment variables first (for non-interactive/CI contexts) before
// falling back to the gh CLI keyring.
//
// Precedence:
//  1. GH_TOKEN environment variable (if set and non-empty)
//  2. GITHUB_TOKEN environment variable (if set and non-empty)
//  3. `gh auth token --user <user>` (keyring lookup)
//
// This matches the precedence used by the `gh` CLI itself, where env
// vars override stored credentials.
func GetTokenWithFallback(user string) (token string, source string, err error) {
	if t := os.Getenv("GH_TOKEN"); t != "" {
		return t, "GH_TOKEN", ValidateToken(t)
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t, "GITHUB_TOKEN", ValidateToken(t)
	}
	t, err := GetToken(user)
	if err != nil {
		return "", "", err
	}
	return t, "keyring", nil
}

// GetTokenInfo resolves the effective token source for user and inspects the
// token scopes using the GitHub API response headers.
func GetTokenInfo(user string) (*TokenInfo, error) {
	token, source, err := GetTokenWithFallback(user)
	if err != nil {
		return nil, err
	}
	scopes, err := GetScopesForToken(token)
	if err != nil {
		return nil, err
	}
	return &TokenInfo{User: user, Source: source, Scopes: scopes}, nil
}

// GetScopesForToken inspects the scopes granted to token using the
// X-OAuth-Scopes response header from the GitHub API.
func GetScopesForToken(token string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create scope inspection request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := scopeInspectionClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect token scopes: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("cannot inspect token scopes: token is unauthorized")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cannot inspect token scopes: github api returned %s", resp.Status)
	}

	raw := strings.TrimSpace(resp.Header.Get("X-OAuth-Scopes"))
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	scopes := make([]string, 0, len(parts))
	for _, part := range parts {
		scope := sanitizeTerminal(strings.TrimSpace(part))
		if scope != "" {
			scopes = append(scopes, scope)
		}
	}
	return scopes, nil
}

// ValidateUser requires the named account's stored credential, not an ambient token.
func ValidateUser(user string) error {
	_, err := GetToken(user)
	if err != nil {
		return fmt.Errorf("user '%s' is not authenticated with gh CLI: %w\nRun: gh auth login --hostname github.com", user, err)
	}
	return nil
}

// NonInteractiveWarning returns a human-readable warning when no token
// source is available in a non-interactive session. Returns "" if a token
// can be resolved or the session is interactive.
func NonInteractiveWarning(user string) string {
	if IsInteractive() {
		return ""
	}
	_, _, err := GetTokenWithFallback(user)
	if err == nil {
		return ""
	}
	return fmt.Sprintf("gh-autoaccount: non-interactive session detected (no TTY).\n"+
		"  Token for '%s' could not be resolved from keyring, and\n"+
		"  neither GH_TOKEN nor GITHUB_TOKEN is set.\n"+
		"  Set one of these environment variables for CI/cron/systemd use.", user)
}

// ListUsers parses `gh auth status` output to list all logged-in accounts.
func ListUsers() ([]UserInfo, error) {
	ghPath, err := ResolveGHPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(ghPath, "auth", "status")
	// gh auth status exits non-zero when there are inactive accounts,
	// but still prints all info — so we always parse the output.
	out, err := cmd.CombinedOutput()
	output := string(out)
	if err != nil && !strings.Contains(output, "Logged in") {
		return nil, fmt.Errorf("cannot get auth status: %w\nOutput: %s", err, output)
	}
	return parseAuthStatus(output), nil
}

// GetGHVersion returns the gh CLI version string (e.g., "2.86.0").
func GetGHVersion() (string, error) {
	ghPath, err := ResolveGHPath()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(ghPath, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh CLI not found: %w", err)
	}
	// Output: "gh version 2.86.0 (2025-02-18)\n..."
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 {
		return "", fmt.Errorf("unexpected gh --version output")
	}
	parts := strings.Fields(lines[0])
	if len(parts) >= 3 {
		return parts[2], nil
	}
	return lines[0], nil
}

// ResolveGHPath returns the real gh binary path, bypassing gh-autoprofile shims.
func ResolveGHPath() (string, error) {
	for _, key := range []string{"GH_PATH", "GH_AUTOPROFILE_REAL_GH"} {
		if path := os.Getenv(key); path != "" {
			return configuredGH(key, path)
		}
	}

	shimPath := ""
	if path, err := shims.GHPath(); err == nil {
		shimPath = path
	}

	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "gh")
		if shimPath != "" && candidate == shimPath {
			continue
		}
		if !validGH(candidate) {
			continue
		}
		return filepath.Abs(candidate)
	}

	return "", fmt.Errorf("gh CLI not found in PATH (or only the gh-autoaccount shim was found)")
}

// configuredGH validates an explicitly configured real gh. A missing path is
// reported separately: generated helpers pin it, and version managers remove
// it when that gh version is uninstalled.
func configuredGH(key, path string) (string, error) {
	if filepath.IsAbs(path) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return "", fmt.Errorf("%s %s no longer exists (was gh upgraded or uninstalled?); refresh with: gh autoaccount setup --migrate", key, path)
		}
	}
	if !filepath.IsAbs(path) || !validGH(path) {
		return "", fmt.Errorf("%s must be an absolute executable path to a real gh (not a shim or this executable)", key)
	}
	return path, nil
}

func validGH(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return false
	}
	if self, err := os.Executable(); err == nil {
		selfInfo, err := os.Stat(self)
		if err != nil || os.SameFile(info, selfInfo) {
			return false
		}
	} else {
		return false
	}
	// The gh-only directory holds nothing but the shim, so even a stale link left
	// by an older extension binary is never a real gh.
	if shims.IsMiseGHPath(path) {
		return false
	}
	if shim, err := shims.GHPath(); err == nil && shims.IsGHInstalled() {
		if shimInfo, err := os.Stat(shim); err == nil && os.SameFile(info, shimInfo) {
			return false
		}
	}
	return !isShimScript(path)
}

func isShimScript(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return false
	}
	content := string(buf[:n])
	return strings.HasPrefix(content, "#!/usr/bin/env bash\n# gh-autoprofile-gh-shim\nset -euo pipefail\n") ||
		(strings.HasPrefix(content, "#!/usr/bin/env bash\nset -euo pipefail\n") && strings.Contains(content, "GH_AUTOPROFILE_DISABLE_SHIM") && strings.Contains(content, "GH_AUTOPROFILE_REAL_GH=") && strings.Contains(content, ` __shim-gh "$@"`))
}

// sanitizeTerminal removes terminal control characters (C0/C1 controls,
// including the ESC introducers of CSI and OSC sequences) from values parsed
// from third-party output before they are displayed. Parsed account data is
// re-emitted by setup/status/doctor, so raw escape sequences reaching stdout
// would let a hijacked `gh` drive the victim's terminal (e.g. OSC 52
// clipboard writes). GitHub usernames cannot contain control characters, so
// real accounts are unaffected.
func sanitizeTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// parseAuthStatus extracts user info from `gh auth status` output.
func parseAuthStatus(output string) []UserInfo {
	var users []UserInfo
	var currentHost string
	lines := strings.Split(output, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Host line: non-indented, contains a dot, no spaces (e.g., "github.com")
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") &&
			strings.Contains(trimmed, ".") && !strings.Contains(trimmed, " ") && trimmed != "" {
			currentHost = sanitizeTerminal(trimmed)
			continue
		}

		// Account line: either a successful login entry or a failed entry.
		if strings.Contains(trimmed, "account") && (strings.Contains(trimmed, "Logged in to") || strings.Contains(trimmed, "Failed to log in to")) {
			parts := strings.SplitN(trimmed, "account ", 2)
			if len(parts) < 2 {
				continue
			}
			rest := parts[1]
			// Username ends at " (" or end of string
			userEnd := strings.Index(rest, " (")
			if userEnd == -1 {
				userEnd = len(rest)
			}
			user := sanitizeTerminal(rest[:userEnd])

			// Look ahead for Active and Protocol lines
			active := false
			protocol := "https"
			valid := !strings.Contains(trimmed, "Failed to log in to")
			var scopes []string
			for j := i + 1; j < len(lines) && j <= i+6; j++ {
				nextLine := strings.TrimSpace(lines[j])
				if strings.Contains(nextLine, "Active account: true") {
					active = true
				}
				if strings.Contains(nextLine, "Active account: false") {
					// explicitly false
				}
				if strings.Contains(nextLine, "Git operations protocol:") {
					colonParts := strings.SplitN(nextLine, ":", 2)
					if len(colonParts) == 2 {
						protocol = sanitizeTerminal(strings.TrimSpace(colonParts[1]))
					}
				}
				if strings.Contains(nextLine, "The token in") && strings.Contains(nextLine, "is invalid") {
					valid = false
				}
				if strings.Contains(nextLine, "Token scopes:") {
					colonParts := strings.SplitN(nextLine, ":", 2)
					if len(colonParts) == 2 {
						raw := strings.TrimSpace(colonParts[1])
						for _, scope := range strings.Split(raw, ",") {
							scope = sanitizeTerminal(strings.TrimSpace(scope))
							scope = strings.Trim(scope, "'")
							if scope != "" {
								scopes = append(scopes, scope)
							}
						}
					}
				}
				// Stop look-ahead at next account or host
				if strings.Contains(nextLine, "Logged in to") || strings.Contains(nextLine, "Failed to log in to") {
					break
				}
			}

			users = append(users, UserInfo{
				User:     user,
				Host:     currentHost,
				Active:   active,
				Protocol: protocol,
				Valid:    valid,
				Scopes:   scopes,
			})
		}
	}
	return users
}
