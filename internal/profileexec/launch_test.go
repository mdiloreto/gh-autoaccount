package profileexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestExecuteReturningAndDirectory(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	env := setEnv(os.Environ(), "PROFILEEXEC_LAUNCH_HELPER", "child")
	err = Execute(context.Background(), dir, []string{exe, "-test.run=^TestLaunchHelper$"}, env, nil, &out, &out)
	var exit ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 {
		t.Fatalf("error %v, output %q", err, out.String())
	}
	if !strings.Contains(out.String(), dir) {
		t.Fatalf("child directory missing: %q", out.String())
	}
}

func TestReplacementPIDAndRelativePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replacement is Unix only")
	}
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "child")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestLaunchHelper$")
	cmd.Env = append(os.Environ(), "PROFILEEXEC_LAUNCH_HELPER=replace", "PROFILEEXEC_LAUNCH_DIR="+dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	err = cmd.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 {
		t.Fatalf("error %v, output %q", err, out.String())
	}
	if !strings.Contains(out.String(), fmt.Sprintf("pid=%d dir=%s", pid, dir)) {
		t.Fatalf("replacement did not preserve PID and directory: %q", out.String())
	}
}

func TestReplacementSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replacement is Unix only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestLaunchHelper$")
	cmd.Env = append(os.Environ(), "PROFILEEXEC_LAUNCH_HELPER=signal")
	err = cmd.Run()
	if err == nil || !strings.Contains(err.Error(), "signal: terminated") {
		t.Fatalf("expected termination by signal, got %v", err)
	}
}

func TestReplacementRestoresDirectoryOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("replacement is Unix only")
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(WithProcessReplacement(context.Background()), t.TempDir(), []string{"./nonexistent"}, os.Environ(), os.Stdin, os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("expected exec failure")
	}
	after, err := os.Getwd()
	if err != nil || after != before {
		t.Fatalf("cwd after failure %q, want %q; %v", after, before, err)
	}
}

func TestLaunchHelper(t *testing.T) {
	switch os.Getenv("PROFILEEXEC_LAUNCH_HELPER") {
	case "replace":
		env := setEnv(os.Environ(), "PROFILEEXEC_LAUNCH_HELPER", "child")
		err := Execute(WithProcessReplacement(context.Background()), os.Getenv("PROFILEEXEC_LAUNCH_DIR"), []string{"./child", "-test.run=^TestLaunchHelper$"}, env, os.Stdin, os.Stdout, os.Stderr)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	case "signal":
		err := Execute(WithProcessReplacement(context.Background()), ".", []string{"/bin/sh", "-c", "kill -TERM $$"}, os.Environ(), os.Stdin, os.Stdout, os.Stderr)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	case "child":
		dir, _ := os.Getwd()
		fmt.Printf("pid=%s dir=%s\n", strconv.Itoa(os.Getpid()), dir)
		os.Exit(37)
	}
}

func TestExecuteRelativePATHBeforeChangingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix symlink and PATH semantics")
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	target := filepath.Join(root, "target")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(bin, "helper")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Error(err)
		}
	})
	t.Setenv("PATH", "bin")
	t.Setenv("GODEBUG", "execerrdot=0")
	env := setEnv(os.Environ(), "PROFILEEXEC_LAUNCH_HELPER", "child")
	var out bytes.Buffer
	err = Execute(context.Background(), target, []string{"helper", "-test.run=^TestLaunchHelper$"}, env, nil, &out, &out)
	var exit ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 37 || !strings.Contains(out.String(), target) {
		t.Fatalf("error %v, output %q", err, out.String())
	}
}
