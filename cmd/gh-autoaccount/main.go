package main

import (
	"context"
	"fmt"
	"os"

	"github.com/mdiloreto/gh-autoaccount/internal/cmd"
	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/profileexec"
)

type exitCoder interface {
	ExitCode() int
}

func main() {
	if err := config.NormalizeEnv(); err != nil {
		fmt.Fprintln(os.Stderr, cmd.SanitizeOutput(err.Error()))
		os.Exit(1)
	}
	command := cmd.NewRootCmd()
	// Running under the proxy's name (gh, or gh.exe on Windows) means proxying gh.
	if config.IsCommandName(os.Args[0], "gh") {
		command = cmd.NewShimGHCMD()
		command.SilenceErrors = true
	}
	if err := command.ExecuteContext(profileexec.WithProcessReplacement(context.Background())); err != nil {
		if exitErr, ok := err.(exitCoder); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, cmd.SanitizeOutput(err.Error()))
		os.Exit(1)
	}
}
