package profileexec

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
)

// ExitError preserves child-process exit codes without printing wrapper errors.
type ExitError struct {
	code int
}

func (e ExitError) Error() string {
	return fmt.Sprintf("process exited with code %d", e.code)
}

func (e ExitError) ExitCode() int {
	return e.code
}

// Context describes the resolved gh-autoaccount state for a command execution.
type Context struct {
	Dir             string
	Pin             *config.Pin
	SelectedUser    string
	SelectionSource string
	SelectedHost    string
	Token           string
	TokenSource     string
}

// ResolveContext resolves the current pin and token source for a directory.
// When requirePin is false, unpinned directories return a context with Pin=nil.
func ResolveContext(dir string, requirePin bool) (*Context, error) {
	return ResolveContextWithPolicy(dir, Policy{RequirePin: requirePin})
}

// Policy controls profile selection and pre-token guards.
type Policy struct {
	User       string
	RequirePin bool
	ExpectUser string
	// Repo enables owner-pin selection for the repository the command accesses.
	Repo *RepoQuery
}

// ResolveContextWithPolicy selects a profile and resolves its credential.
func ResolveContextWithPolicy(dir string, policy Policy) (*Context, error) {
	absDir, err := config.CanonicalDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve directory: %w", err)
	}

	var selectedPin *config.Pin
	var pinSource string
	if policy.User == "" || policy.RequirePin {
		registry, err := config.LoadPins()
		if err != nil {
			return nil, fmt.Errorf("cannot load pin registry: %w", err)
		}
		selectedPin, pinSource, err = SelectPin(registry, dir, policy.Repo)
		if err != nil {
			return nil, fmt.Errorf("cannot select pin: %w", err)
		}
	}
	if policy.RequirePin && selectedPin == nil {
		return nil, fmt.Errorf("no pin found for directory: %s (--require-pin)", absDir)
	}
	if policy.User != "" && policy.RequirePin && !strings.EqualFold(policy.User, selectedPin.User) {
		kind := "directory"
		if pinSource == "owner" {
			kind = "owner"
		}
		return nil, fmt.Errorf("--user %q does not match %s pin", policy.User, kind)
	}
	var pin *config.Pin
	var selectedUser, selectionSource string
	if policy.User != "" && !policy.RequirePin {
		selectedUser, selectionSource = policy.User, "explicit"
	} else if selectedPin != nil {
		pin = selectedPin
		selectedUser, selectionSource = pin.User, pinSource
	} else {
		settings, loadErr := config.LoadSettings()
		if loadErr != nil {
			return nil, fmt.Errorf("cannot load setup settings: %w", loadErr)
		}
		selectedUser = settings.DefaultUser
		if selectedUser != "" {
			selectionSource = "default"
		}
	}
	if selectedUser == "" {
		if policy.ExpectUser != "" {
			return nil, fmt.Errorf("--expect-user %q: no profile selected for directory: %s", policy.ExpectUser, absDir)
		}
		return &Context{Dir: absDir, SelectionSource: "none"}, nil
	}
	if policy.ExpectUser != "" && !strings.EqualFold(selectedUser, policy.ExpectUser) {
		return nil, fmt.Errorf("--expect-user %q: selected profile is %q", policy.ExpectUser, selectedUser)
	}

	var token, source string
	// Ambient credentials are an explicit export-mode opt-in. Without it, the
	// named pin always resolves its own stored credential.
	allowAmbient := os.Getenv("GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN") == "1"
	request := ghauth.TokenRequest{User: selectedUser}
	if pin != nil {
		request.Host, request.TokenCommand = pin.Host, pin.TokenCommand
	}
	host, err := config.NormalizeHost(request.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid selected pin host: %w", err)
	}
	markerHost := os.Getenv("GH_AUTOPROFILE_HOST")
	markerUser := os.Getenv("GH_AUTOPROFILE_USER")
	markerMatches := false
	if markerHost != "" && markerUser != "" {
		normalized, normalizeErr := config.NormalizeHost(markerHost)
		markerMatches = normalizeErr == nil && normalized == host && strings.EqualFold(markerUser, selectedUser)
	}
	if policy.User == "" && pin != nil && pin.TokenCommand == "" && pin.EffectiveMode() == config.ModeExport && allowAmbient && markerMatches {
		token, source, err = ghauth.GetTokenWithFallbackForHost(context.Background(), request)
	} else {
		token, err = ghauth.ResolveToken(context.Background(), request)
		source = "keyring"
		if request.TokenCommand != "" {
			source = "token_command"
		}
	}
	result := &Context{
		Dir:             absDir,
		Pin:             pin,
		SelectedUser:    selectedUser,
		SelectionSource: selectionSource,
		SelectedHost:    host,
		Token:           token,
		TokenSource:     source,
	}
	if err != nil {
		return result, fmt.Errorf("cannot resolve token for '%s': %w", selectedUser, err)
	}
	return result, nil
}

// Run executes argv in the resolved profile context for dir.
func Run(dir string, argv []string, requirePin bool) error {
	if len(argv) == 0 {
		return fmt.Errorf("no command provided")
	}

	ctx, err := ResolveContext(dir, requirePin)
	if err != nil {
		return err
	}

	commandPath, err := resolveCommandPath(argv[0])
	if err != nil {
		return err
	}

	env, err := buildEnv(ctx, filepath.Base(commandPath) == "git")
	if err != nil {
		return err
	}

	return Execute(context.Background(), ctx.Dir, append([]string{commandPath}, argv[1:]...), env, os.Stdin, os.Stdout, os.Stderr)
}

// RunUnpinned executes argv without profile injection, preserving the current
// environment while normalizing the working directory and exit-code behavior.
func RunUnpinned(dir string, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("no command provided")
	}
	absDir, err := config.CanonicalDir(dir)
	if err != nil {
		return fmt.Errorf("cannot resolve directory: %w", err)
	}
	return Execute(context.Background(), absDir, argv, append([]string{}, os.Environ()...), os.Stdin, os.Stdout, os.Stderr)
}

func resolveCommandPath(command string) (string, error) {
	if filepath.Base(command) == "gh" {
		path, err := ghauth.ResolveGHPath()
		if err != nil {
			return "", fmt.Errorf("cannot resolve real gh binary: %w", err)
		}
		if filepath.Base(path) == path {
			path, err = osexec.LookPath(path)
			if err != nil {
				return "", err
			}
		}
		return filepath.Abs(path)
	}

	path, err := osexec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("cannot find command %q: %w", command, err)
	}
	return filepath.Abs(path)
}

func buildEnv(ctx *Context, forGit bool) ([]string, error) {
	return BuildEnv(ctx, forGit)
}

// BuildExecEnv prepares credentials for scripts and their Git descendants.
func BuildExecEnv(ctx *Context) ([]string, error) {
	env, err := BuildEnv(ctx, false)
	if err != nil || (ctx.Pin == nil && ctx.SelectedUser == "") {
		return env, err
	}
	host := ctx.SelectedHost
	if ctx.Pin != nil {
		host = ctx.Pin.Host
	}
	env, err = ghauth.CredentialEnv(env, host, ctx.Token, true)
	if err != nil {
		return nil, err
	}
	return WithHostCredentials(env, host)
}

// BuildEnv prepares the environment for shim execution.
func BuildEnv(ctx *Context, forGit bool) ([]string, error) {
	env := append([]string{}, os.Environ()...)
	env = setEnv(env, "GH_AUTOPROFILE_DISABLE_SHIM", "1")
	if realGH, err := resolveCommandPath("gh"); err == nil {
		env = setEnv(env, "GH_AUTOPROFILE_REAL_GH", realGH)
	}
	if ctx.Pin == nil && ctx.SelectedUser == "" {
		if os.Getenv("GH_AUTOPROFILE_USER") != "" || os.Getenv("GH_AUTOPROFILE_HOST") != "" || os.Getenv("GH_AUTOPROFILE_MODE") != "" {
			for _, key := range []string{"GH_AUTOPROFILE_USER", "GH_AUTOPROFILE_HOST", "GH_AUTOPROFILE_MODE", "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
				env = removeEnv(env, key)
			}
		}
		return WithoutGitHubCredentials(env)
	}

	selectedUser, selectedHost := ctx.SelectedUser, ctx.SelectedHost
	mode := config.ModeWrapper
	if ctx.Pin != nil {
		selectedUser, selectedHost, mode = ctx.Pin.User, ctx.Pin.Host, ctx.Pin.EffectiveMode()
	}
	host, err := config.NormalizeHost(selectedHost)
	if err != nil {
		return nil, err
	}
	env, err = ghauth.CredentialEnv(env, host, ctx.Token, false)
	if err != nil {
		return nil, err
	}
	env = setEnv(env, "GH_AUTOPROFILE_USER", selectedUser)
	env = setEnv(env, "GH_AUTOPROFILE_HOST", host)
	env = setEnv(env, "GH_AUTOPROFILE_MODE", string(mode))
	env, err = WithoutGitHubCredentials(env)
	if err != nil {
		return nil, err
	}

	settings, err := config.LoadSettings()
	if err != nil {
		return nil, fmt.Errorf("cannot load setup settings: %w", err)
	}
	if ctx.Pin == nil {
		if forGit {
			return WithHostCredentials(env, host)
		}
		return env, nil
	}
	if !settings.GitIdentity && ctx.Pin.GitEmail != "" {
		env = setEnv(env, "GIT_AUTHOR_EMAIL", ctx.Pin.GitEmail)
		env = setEnv(env, "GIT_COMMITTER_EMAIL", ctx.Pin.GitEmail)
	}
	if !settings.GitIdentity && ctx.Pin.GitName != "" {
		env = setEnv(env, "GIT_AUTHOR_NAME", ctx.Pin.GitName)
		env = setEnv(env, "GIT_COMMITTER_NAME", ctx.Pin.GitName)
	}
	if ctx.Pin.SSHKey != "" {
		env = setEnv(env, "GIT_SSH_COMMAND", buildSSHCommand(ctx.Pin.SSHKey))
	}

	if forGit {
		var err error
		env, err = WithHostCredentials(env, host)
		if err != nil {
			return nil, err
		}
		var pairs [][2]string
		if !settings.GitIdentity && ctx.Pin.GitEmail != "" {
			pairs = append(pairs, [2]string{"user.email", ctx.Pin.GitEmail})
		}
		if !settings.GitIdentity && ctx.Pin.GitName != "" {
			pairs = append(pairs, [2]string{"user.name", ctx.Pin.GitName})
		}
		if ctx.Pin.SSHKey != "" {
			pairs = append(pairs, [2]string{"core.sshCommand", buildSSHCommand(ctx.Pin.SSHKey)})
		}
		env, err = appendGitConfigPairs(env, pairs)
		if err != nil {
			return nil, err
		}
		return env, nil
	}

	return env, nil
}

const gitHubConfigStart = "GH_AUTOPROFILE_GIT_CONFIG_START"
const gitHubConfigHost = "GH_AUTOPROFILE_GIT_CONFIG_HOST"

var gitHubCredentialPairs = [][2]string{
	{"credential.https://github.com.helper", ""},
	{"credential.https://github.com.helper", `!f() {
test "$1" = get || exit 0
protocol= host=
while IFS= read -r line && test -n "$line"; do
  case "$line" in protocol=*) protocol=${line#protocol=} ;; host=*) host=${line#host=} ;; esac
done
test "$protocol" = https && test "$host" = github.com || exit 0
test -n "$GH_TOKEN" || { printf 'quit=true\n\n'; exit 1; }
printf 'username=x-access-token\npassword=%s\n\n' "$GH_TOKEN"
}; f`},
	{"credential.https://github.com.username", "x-access-token"},
}

// WithGitHubCredentials keeps the resolved token authoritative for child Git commands.
func WithGitHubCredentials(env []string) ([]string, error) {
	return WithHostCredentials(env, "github.com")
}

func hostCredentialPairs(host string) [][2]string {
	primary := "GH_ENTERPRISE_TOKEN"
	if host == "github.com" || strings.HasSuffix(host, ".ghe.com") {
		primary = "GH_TOKEN"
	}
	pairs := make([][2]string, len(gitHubCredentialPairs))
	for i, pair := range gitHubCredentialPairs {
		pairs[i] = [2]string{strings.ReplaceAll(pair[0], "github.com", host), strings.ReplaceAll(strings.ReplaceAll(pair[1], "github.com", host), "GH_TOKEN", primary)}
	}
	return pairs
}

// WithHostCredentials installs an exact-host credential block for the selected host.
func WithHostCredentials(env []string, host string) ([]string, error) {
	host, err := config.NormalizeHost(host)
	if err != nil {
		return nil, err
	}
	env, err = WithoutGitHubCredentials(env)
	if err != nil {
		return nil, err
	}
	pairs := hostCredentialPairs(host)
	env, err = appendGitConfigPairs(env, pairs)
	if err != nil {
		return nil, err
	}
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "GIT_CONFIG_COUNT="); ok {
			count, _ := strconv.Atoi(value) // Validated by appendGitConfigPairs.
			env = setEnv(env, gitHubConfigStart, strconv.Itoa(count-len(pairs)))
			return setEnv(env, gitHubConfigHost, host), nil
		}
	}
	return env, nil
}

// WithoutGitHubCredentials removes only our recorded block, retaining later caller config.
func WithoutGitHubCredentials(env []string) ([]string, error) {
	values := make(map[string]string, len(env))
	duplicates := make(map[string]bool)
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if _, exists := values[key]; exists {
			duplicates[key] = true
		}
		values[key] = value
	}
	marker, ok := values[gitHubConfigStart]
	if !ok {
		if _, present := values[gitHubConfigHost]; present {
			return nil, fmt.Errorf("invalid inherited gh-autoaccount Git config block")
		}
		return env, nil
	}
	if duplicates[gitHubConfigStart] || duplicates[gitHubConfigHost] || duplicates["GIT_CONFIG_COUNT"] {
		return nil, fmt.Errorf("duplicate inherited gh-autoaccount Git config metadata")
	}
	host := "github.com"
	if recorded, present := values[gitHubConfigHost]; present {
		var err error
		host, err = config.NormalizeHost(recorded)
		if err != nil || recorded != host {
			return nil, fmt.Errorf("invalid inherited gh-autoaccount Git config host")
		}
	}
	pairs := hostCredentialPairs(host)
	start, startErr := strconv.Atoi(marker)
	count, countErr := strconv.Atoi(values["GIT_CONFIG_COUNT"])
	size := len(pairs)
	if startErr != nil || countErr != nil || start < 0 || count < size || start > count-size {
		return nil, fmt.Errorf("invalid inherited gh-autoaccount Git config block")
	}
	for key := range duplicates {
		if strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_") {
			return nil, fmt.Errorf("duplicate inherited Git config entry")
		}
	}
	for key, value := range values {
		index, ok := strings.CutPrefix(key, "GIT_CONFIG_KEY_")
		if !ok {
			continue
		}
		idx, err := strconv.Atoi(index)
		if err != nil || idx < 0 || idx >= count || idx == start+1 {
			continue
		}
		helperHost, ok := strings.CutPrefix(value, "credential.https://")
		if !ok {
			continue
		}
		helperHost, ok = strings.CutSuffix(helperHost, ".helper")
		normalized, hostErr := config.NormalizeHost(helperHost)
		if ok && hostErr == nil && normalized == helperHost && values["GIT_CONFIG_VALUE_"+index] == hostCredentialPairs(normalized)[1][1] {
			return nil, fmt.Errorf("duplicate inherited gh-autoaccount Git config block")
		}
	}
	for i, pair := range pairs {
		keyName := fmt.Sprintf("GIT_CONFIG_KEY_%d", start+i)
		valueName := fmt.Sprintf("GIT_CONFIG_VALUE_%d", start+i)
		key, keyOK := values[keyName]
		value, valueOK := values[valueName]
		if !keyOK || !valueOK || duplicates[keyName] || duplicates[valueName] || key != pair[0] || value != pair[1] {
			return nil, fmt.Errorf("inherited gh-autoaccount Git config block was modified")
		}
	}
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if key == gitHubConfigStart || key == gitHubConfigHost {
			continue
		}
		remove := false
		for _, prefix := range []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"} {
			if suffix, ok := strings.CutPrefix(key, prefix); ok {
				idx, err := strconv.Atoi(suffix)
				if err == nil && idx >= start && idx < count {
					if idx < start+size {
						remove = true
					} else {
						entry = fmt.Sprintf("%s%d=%s", prefix, idx-size, value)
					}
				}
			}
		}
		if !remove {
			result = append(result, entry)
		}
	}
	return setEnv(result, "GIT_CONFIG_COUNT", strconv.Itoa(count-size)), nil
}

func appendGitConfigPairs(env []string, pairs [][2]string) ([]string, error) {
	start := 0
	seenCount := false
	for _, entry := range env {
		if count, ok := strings.CutPrefix(entry, "GIT_CONFIG_COUNT="); ok {
			if seenCount {
				return nil, fmt.Errorf("duplicate GIT_CONFIG_COUNT")
			}
			seenCount = true
			if count == "" {
				continue
			}
			parsed, err := strconv.Atoi(count)
			if err != nil || parsed < 0 || parsed > int(^uint(0)>>1)-len(pairs) {
				return nil, fmt.Errorf("invalid GIT_CONFIG_COUNT: %q", count)
			}
			start = parsed
		}
	}

	env = setEnv(env, "GIT_CONFIG_COUNT", strconv.Itoa(start+len(pairs)))
	for i, pair := range pairs {
		idx := start + i
		env = setEnv(env, fmt.Sprintf("GIT_CONFIG_KEY_%d", idx), pair[0])
		env = setEnv(env, fmt.Sprintf("GIT_CONFIG_VALUE_%d", idx), pair[1])
	}
	return env, nil
}

func setEnv(env []string, key, value string) []string {
	return append(removeEnv(env, key), key+"="+value)
}

func removeEnv(env []string, key string) []string {
	prefix := key + "="
	filtered := env[:0]
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func buildSSHCommand(sshKey string) string {
	escaped := strings.ReplaceAll(sshKey, `'`, `'\''`)
	return fmt.Sprintf("ssh -i '%s' -o IdentitiesOnly=yes", escaped)
}
