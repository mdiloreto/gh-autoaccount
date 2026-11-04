package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/ghauth"
	"github.com/spf13/cobra"
)

// NewGitCredentialCmd creates the hidden git credential helper entrypoint.
func NewGitCredentialCmd() *cobra.Command {
	return &cobra.Command{
		Use:                "__git-credential",
		Hidden:             true,
		DisableFlagParsing: true,
		Args:               cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			action := "get"
			if len(args) > 0 && args[0] != "" {
				action = args[0]
			}

			req, err := readCredentialRequest(os.Stdin)
			if err != nil {
				fmt.Fprint(os.Stdout, "quit=true\n\n")
				return err
			}

			switch action {
			case "store", "erase":
				return nil
			case "get", "fill":
				return handleGitCredentialGet(req)
			default:
				return nil
			}
		},
	}
}

type credentialRequest struct {
	protocol string
	host     string
	path     string

	original map[string]string
}

func readCredentialRequest(r *os.File) (*credentialRequest, error) {
	req := &credentialRequest{original: map[string]string{}}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		req.original[key] = value
		switch key {
		case "protocol":
			req.protocol = value
		case "host":
			req.host = value
		case "path":
			req.path = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("cannot read git credential request: %w", err)
	}
	return req, nil
}

func handleGitCredentialGet(req *credentialRequest) error {
	if req.protocol != "https" {
		return nil
	}

	registry, err := config.LoadPins()
	if err != nil {
		fmt.Fprint(os.Stdout, "quit=true\n\n")
		return err
	}
	// Git runs helpers in its invoking directory, not a clone's destination,
	// so the repository owner in the request path outranks the cwd selection.
	var pin *config.Pin
	if req.host != "" {
		owner, _, _ := strings.Cut(req.path, "/")
		pin = registry.FindOwnerPin(req.host, strings.TrimSuffix(owner, ".git"))
	}
	if pin == nil {
		pin, err = registry.FindPinStrict(".")
		if err != nil {
			fmt.Fprint(os.Stdout, "quit=true\n\n")
			return err
		}
	}
	user, selectedHost, tokenCommand := "", "", ""
	if pin != nil {
		user, selectedHost, tokenCommand = pin.User, pin.Host, pin.TokenCommand
	} else {
		settings, loadErr := config.LoadSettings()
		if loadErr != nil {
			fmt.Fprint(os.Stdout, "quit=true\n\n")
			return loadErr
		}
		if settings.DefaultUser == "" {
			return nil
		}
		user = settings.DefaultUser
	}
	host, err := config.NormalizeHost(selectedHost)
	requestHost, hostErr := config.NormalizeHost(req.host)
	if err != nil || hostErr != nil || req.host == "" || host != requestHost {
		fmt.Fprint(os.Stdout, "quit=true\n\n")
		return fmt.Errorf("credential host does not match selected pin")
	}
	token, err := ghauth.ResolveToken(context.Background(), ghauth.TokenRequest{User: user, Host: host, TokenCommand: tokenCommand})
	if err != nil {
		fmt.Fprint(os.Stdout, "quit=true\n\n")
		// Only gh resolution errors are surfaced: they name a path, never credential output.
		if _, ghErr := ghauth.ResolveGHPath(); tokenCommand == "" && ghErr != nil {
			return fmt.Errorf("cannot resolve selected pin credential: %w", ghErr)
		}
		return fmt.Errorf("cannot resolve selected pin credential")
	}

	if _, err := fmt.Fprintln(os.Stdout, "username=x-access-token"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "password=%s\n", token); err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout)
	return err
}
