package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/profileexec"
	"github.com/spf13/cobra"
)

type pinDiagnostics struct {
	User                   string         `json:"user"`
	Host                   string         `json:"host"`
	TokenCommandConfigured bool           `json:"token_command_configured"`
	Directory              string         `json:"directory,omitempty"`
	Owner                  string         `json:"owner,omitempty"`
	Mode                   config.PinMode `json:"mode"`
	GitName                string         `json:"git_name,omitempty"`
	GitEmail               string         `json:"git_email,omitempty"`
	SSHKey                 string         `json:"ssh_key,omitempty"`
}

func describePin(pin *config.Pin) *pinDiagnostics {
	if pin == nil {
		return nil
	}
	host, err := config.NormalizeHost(pin.Host)
	if err != nil {
		host = "(invalid)"
	}
	return &pinDiagnostics{
		User: pin.User, Directory: pin.Dir, Owner: pin.Owner, Mode: pin.EffectiveMode(),
		Host: host, TokenCommandConfigured: pin.TokenCommand != "",
		GitName: pin.GitName, GitEmail: pin.GitEmail, SSHKey: pin.SSHKey,
	}
}

type diagnosticError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type environmentDiagnostics struct {
	ProfileUser                  string `json:"profile_user"`
	ProfileMode                  string `json:"profile_mode"`
	ProfileHost                  string `json:"profile_host"`
	GHTokenPresent               bool   `json:"gh_token_present"`
	GitHubTokenPresent           bool   `json:"github_token_present"`
	GHEnterpriseTokenPresent     bool   `json:"gh_enterprise_token_present"`
	GitHubEnterpriseTokenPresent bool   `json:"github_enterprise_token_present"`
}

type authenticationDiagnostics struct {
	Selected            bool             `json:"selected"`
	Source              string           `json:"source"`
	CredentialAvailable bool             `json:"credential_available"`
	Verified            bool             `json:"verified"`
	Error               *diagnosticError `json:"error,omitempty"`
}

type gitDiagnostics struct {
	GitDir         string           `json:"git_dir,omitempty"`
	AuthorIdent    string           `json:"author_ident,omitempty"`
	CommitterIdent string           `json:"committer_ident,omitempty"`
	Error          *diagnosticError `json:"error,omitempty"`
}

type statusDiagnostics struct {
	SchemaVersion   int             `json:"schema_version"`
	Directory       string          `json:"directory"`
	SelectedUser    string          `json:"selected_user"`
	SelectionSource string          `json:"selection_source"`
	Pin             *pinDiagnostics `json:"pin"`
	// ConflictingOwnerPins lists owner pins matched by remotes whose Git identity
	// can disagree with the selected credentials.
	ConflictingOwnerPins []string                  `json:"conflicting_owner_pins,omitempty"`
	Environment          environmentDiagnostics    `json:"environment"`
	Authentication       authenticationDiagnostics `json:"authentication"`
	Git                  gitDiagnostics            `json:"git"`
}

func diagnosticsPreRun(cmd *cobra.Command, args []string) error {
	jsonOutput, _ := cmd.Flags().GetBool("json")
	if !jsonOutput {
		if _, err := config.LoadSettings(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "gh-autoaccount: %v\n", diagnosticConfigError("setup settings", "settings.yml", err))
			return nil
		}
		warnUpgradeDrift(cmd)
	}
	return nil
}

func diagnosticConfigError(label, filename string, err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("cannot load %s: cannot read %s; check its location and read permissions", label, filename)
	}
	return fmt.Errorf("cannot load %s: check %s YAML syntax, supported field names, and value types", label, filename)
}

func collectStatus(dir string) (*statusDiagnostics, error) {
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot get current directory; use --dir with an accessible directory")
		}
	}
	absDir, err := config.CanonicalDir(dir)
	if err != nil {
		return nil, fmt.Errorf("target directory does not exist or is not accessible; check --dir")
	}
	if _, err := config.LoadPins(); err != nil {
		return nil, diagnosticConfigError("pin registry", "pins.yml", err)
	}
	if _, err := config.LoadSettings(); err != nil {
		return nil, diagnosticConfigError("setup settings", "settings.yml", err)
	}
	// One resolution selects the pin and reads remotes with a single git process.
	ctx, err := profileexec.ResolveContextWithPolicy(absDir, profileexec.Policy{Repo: &profileexec.RepoQuery{GH: true, Conflicts: true}})
	if ctx == nil {
		return nil, fmt.Errorf("cannot select directory pin: %w", err)
	}
	result := &statusDiagnostics{
		SchemaVersion:        1,
		Directory:            absDir,
		SelectionSource:      "none",
		Pin:                  describePin(ctx.Pin),
		SelectedUser:         ctx.SelectedUser,
		ConflictingOwnerPins: ctx.OwnerConflicts,
		Environment: environmentDiagnostics{
			ProfileUser: os.Getenv("GH_AUTOPROFILE_USER"), ProfileMode: os.Getenv("GH_AUTOPROFILE_MODE"),
			ProfileHost:    os.Getenv("GH_AUTOPROFILE_HOST"),
			GHTokenPresent: os.Getenv("GH_TOKEN") != "", GitHubTokenPresent: os.Getenv("GITHUB_TOKEN") != "",
			GHEnterpriseTokenPresent: os.Getenv("GH_ENTERPRISE_TOKEN") != "", GitHubEnterpriseTokenPresent: os.Getenv("GITHUB_ENTERPRISE_TOKEN") != "",
		},
		Authentication: authenticationDiagnostics{Source: "default"},
		Git:            inspectGit(absDir),
	}
	if ctx.SelectedUser != "" {
		result.SelectionSource = ctx.SelectionSource
	}
	if err != nil {
		result.Authentication.Selected = result.SelectedUser != ""
		result.Authentication.Source = "unavailable"
		result.Authentication.Error = &diagnosticError{
			Code: "credential_unavailable", Message: "Cannot resolve the selected profile's credential locally. Check the configured token source or run gh auth login --hostname for the selected host.",
		}
		return result, nil
	}
	if ctx.SelectedUser != "" {
		result.Authentication.Selected = true
		result.Authentication.Source = ctx.TokenSource
		result.Authentication.CredentialAvailable = ctx.Token != ""
	}
	return result, nil
}

func inspectGit(dir string) gitDiagnostics {
	read := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSuffix(string(out), "\n"), nil
	}
	gitDir, err := read("rev-parse", "--absolute-git-dir")
	if err != nil {
		return gitDiagnostics{Error: &diagnosticError{
			Code: "repository_unavailable", Message: "Cannot resolve a Git directory; check that the target is a repository, Git is installed, and Git configuration is valid.",
		}}
	}
	result := gitDiagnostics{GitDir: gitDir}
	author, authorErr := read("var", "GIT_AUTHOR_IDENT")
	committer, committerErr := read("var", "GIT_COMMITTER_IDENT")
	result.AuthorIdent, result.CommitterIdent = author, committer
	if authorErr != nil || committerErr != nil {
		result.Error = &diagnosticError{Code: "identity_unavailable", Message: "Cannot resolve one or more Git author/committer identities; check user.name, user.email, and Git identity environment overrides."}
	}
	return result
}
