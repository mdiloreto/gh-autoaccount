package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	"github.com/mdiloreto/gh-autoaccount/internal/shims"
)

// miseExecutable locates mise on PATH; tests replace it.
var miseExecutable = func() (string, error) { return exec.LookPath("mise") }

const miseTimeout = 10 * time.Second

// runMise returns mise's stdout. Every call is bounded so a hung mise cannot
// stall setup or doctor.
func runMise(args ...string) (string, error) {
	bin, err := miseExecutable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), miseTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...)
	// Bounds the wait when a child outlives mise and keeps its pipe open.
	c.WaitDelay = time.Second
	// A diagnostic probe inside a project directory must not install that
	// project's tools, which would use the network.
	c.Env = append(os.Environ(), "MISE_AUTO_INSTALL=0", "MISE_EXEC_AUTO_INSTALL=0")
	out, err := c.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("mise %s timed out after %s", args[0], miseTimeout)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("mise %s failed: %w: %s", args[0], err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("mise %s failed: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// These are the only mise commands that edit configuration. mise's own
// --append and --remove make both idempotent and leave other entries alone.
func miseRegister(dir string) error {
	_, err := runMise("config", "set", "-g", "--append", "env._.path", dir)
	return err
}

func miseUnregister(dir string) error {
	_, err := runMise("config", "set", "-g", "--remove", "env._.path", dir)
	return err
}

// parseMisePath turns `mise config get` output for env._.path into its entries.
// mise prints a scalar string bare and an array as `["a", "b"]` without JSON
// escaping, so the array is split on its separators instead of JSON-decoded.
// Any other shape is an error rather than a guess.
func parseMisePath(out string) ([]string, error) {
	switch {
	case strings.HasPrefix(out, "["):
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(out, "["), "]"))
		if !strings.HasSuffix(out, "]") || (inner != "" && (len(inner) < 2 || !strings.HasPrefix(inner, `"`) || !strings.HasSuffix(inner, `"`))) {
			return nil, fmt.Errorf("unsupported mise env._.path value %q: expected a list of strings", out)
		}
		if inner == "" {
			return nil, nil
		}
		return strings.Split(inner[1:len(inner)-1], `", "`), nil
	case strings.HasPrefix(out, "{"):
		return nil, fmt.Errorf("unsupported mise env._.path value %q: expected a string or a list of strings", out)
	case out == "":
		return nil, nil
	}
	return []string{out}, nil
}

// miseRegistered reads whether dir is currently in mise's global env._.path, so
// rollback can restore exactly what was there rather than infer it. A missing
// key is mise's way of saying the list is empty.
func miseRegistered(dir string) (bool, error) {
	out, err := runMise("config", "get", "-g", "env._.path")
	if err != nil {
		if strings.Contains(err.Error(), "Key not found") {
			return false, nil
		}
		return false, err
	}
	entries, err := parseMisePath(out)
	if err != nil {
		return false, err
	}
	return slices.Contains(entries, dir), nil
}

// applyMise reconciles the gh-only shim and its mise registration with the
// requested state. Every mutation registers its undo through addUndo, so a later
// setup failure restores what this step changed. Undos that depend on mise are
// registered before the call they undo, because the mise commands are idempotent
// and a failing mise may still have written. A missing mise is a warning: runSetup
// rejects it up front when the user asked for --mise explicitly.
func applyMise(enable bool, extensionBinary string, addUndo func(func() error)) (string, error) {
	dir, err := shims.MiseGHDir()
	if err != nil {
		return "", err
	}
	path, err := shims.MiseGHPath()
	if err != nil {
		return "", err
	}
	if err := shims.CheckMiseGHLayout(); err != nil {
		return "", err
	}
	snapshot, err := snapshotSetupFile(path)
	if err != nil {
		return "", err
	}
	registered := false
	_, lookErr := miseExecutable()
	if lookErr == nil {
		if registered, err = miseRegistered(dir); err != nil {
			return "", fmt.Errorf("cannot read mise's global config: %w", err)
		}
	}

	if !enable {
		if lookErr != nil {
			return "mise not found on PATH; remove the env._.path entry for " + dir + " from mise's global config if it exists", removeMiseShim(snapshot, addUndo)
		}
		if registered {
			addUndo(func() error { return miseRegister(dir) })
		}
		if err := miseUnregister(dir); err != nil {
			return "", fmt.Errorf("cannot unregister gh-only shim directory from mise: %w", err)
		}
		return "", removeMiseShim(snapshot, addUndo)
	}

	if snapshot.exists {
		addUndo(snapshot.restore)
	} else {
		addUndo(func() error { _, err := shims.RemoveMiseGH(); return err })
	}
	if _, err := shims.InstallMiseGH(extensionBinary); err != nil {
		return "", fmt.Errorf("cannot install gh-only shim for mise: %w", err)
	}
	if lookErr != nil {
		return "mise not found on PATH; the gh-only shim was refreshed but mise's config was not checked", nil
	}
	if !registered {
		addUndo(func() error { return miseUnregister(dir) })
	}
	if err := miseRegister(dir); err != nil {
		return "", fmt.Errorf("cannot register gh-only shim directory with mise: %w", err)
	}
	return "", nil
}

func removeMiseShim(snapshot setupFileSnapshot, addUndo func(func() error)) error {
	removed, err := shims.RemoveMiseGH()
	if removed {
		addUndo(func() error {
			// Removal also pruned the emptied directories.
			if err := os.MkdirAll(filepath.Dir(snapshot.path), 0755); err != nil {
				return err
			}
			return snapshot.restore()
		})
	}
	return err
}

// underMise reports whether path lives in mise's installs or shims directories.
func underMise(path string) bool {
	if strings.Contains(path, "/mise/installs/") || strings.Contains(path, "/mise/shims/") {
		return true
	}
	data := os.Getenv("MISE_DATA_DIR")
	return data != "" && strings.HasPrefix(path, filepath.Clean(data)+string(filepath.Separator))
}

// shadowFixHint says how to make plain gh reach the managed shim, given what it
// resolved to instead.
func shadowFixHint(resolved string, settings config.Settings) string {
	switch {
	case settings.Mise:
		return "mise integration is enabled but not effective here; run: gh autoaccount setup --migrate, then start a new shell"
	case underMise(resolved):
		return "mise provides this gh; run: gh autoaccount setup --mise"
	}
	dir, err := shims.Dir()
	if err != nil {
		return "put the managed gh shim directory before it on PATH"
	}
	return "put " + dir + " before " + filepath.Dir(resolved) + " on PATH"
}

// checkGHOnPath prints whether plain gh reaches a managed shim, both in this
// process's PATH and inside mise's environment when mise is present. verified is
// false when gh is not on PATH at all, so the caller keeps its generic output.
func checkGHOnPath(settings config.Settings) (verified bool, issues int) {
	resolved, err := exec.LookPath("gh")
	if err != nil {
		return false, 0
	}
	if shims.IsManagedGHShim(resolved) {
		fmt.Println("OK   plain gh resolves to the managed shim")
		fmt.Printf("     path: %s\n", resolved)
	} else {
		fmt.Printf("WARN plain gh resolves to %s, not the managed shim\n", resolved)
		fmt.Println("     " + shadowFixHint(resolved, settings))
		issues++
	}
	if _, err := miseExecutable(); err != nil {
		return true, issues
	}
	inMise, err := runMise("exec", "--", "sh", "-c", "command -v gh")
	switch {
	case err != nil:
		fmt.Printf("INFO cannot check gh inside mise exec: %v\n", err)
	case shims.IsManagedGHShim(inMise):
		fmt.Println("OK   gh inside mise exec resolves to the managed shim")
	default:
		fmt.Printf("WARN gh inside mise exec resolves to %s, not the managed shim\n", inMise)
		fmt.Println("     " + shadowFixHint(inMise, settings))
		issues++
	}
	return true, issues
}
