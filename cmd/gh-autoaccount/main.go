package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mdiloreto/gh-autoaccount/internal/cmd"
	"github.com/mdiloreto/gh-autoaccount/internal/profileexec"
)

type exitCoder interface {
	ExitCode() int
}

func main() {
	command := cmd.NewRootCmd()
	if filepath.Base(os.Args[0]) == "gh" {
		command = cmd.NewShimGHCMD()
	}
	if err := command.ExecuteContext(profileexec.WithProcessReplacement(context.Background())); err != nil {
		if exitErr, ok := err.(exitCoder); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
