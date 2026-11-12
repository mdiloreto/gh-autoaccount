package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// miseEntries reads mise's global env._.path; a missing key means an empty list.
func miseEntries() ([]string, error) {
	out, err := runMise("config", "get", "-g", "env._.path")
	if err != nil {
		if strings.Contains(err.Error(), "Key not found") {
			return nil, nil
		}
		return nil, err
	}
	return parseMisePath(out)
}

// legacyMiseGHDirs finds relative registrations left by pre-normalization setup runs.
func legacyMiseGHDirs(entries []string) []string {
	var legacy []string
	for _, entry := range entries {
		if filepath.IsAbs(entry) {
			continue
		}
		// mise expands these outside the working directory, so they are not the
		// relative registration this tool created.
		if entry != "" {
			switch entry[0] {
			case '~', '$', '%':
				continue
			}
		}
		clean := filepath.Clean(entry)
		if filepath.Base(clean) == "bin" && filepath.Base(filepath.Dir(clean)) == "gh-autoaccount" {
			legacy = append(legacy, entry)
		}
	}
	return legacy
}

func countMiseEntry(entries []string, want string) int {
	count := 0
	for _, entry := range entries {
		if entry == want {
			count++
		}
	}
	return count
}

// removeLegacyMiseEntries removes each legacy entry and returns the entries whose
// parsed count actually decreased, in original order. The caller restores them
// with one undo so rollback preserves their relative order.
func removeLegacyMiseEntries(entries []string) ([]string, error) {
	var removed []string
	for _, entry := range entries {
		before, err := miseEntries()
		if err != nil {
			return removed, err
		}
		beforeCount := countMiseEntry(before, entry)
		if beforeCount == 0 {
			continue
		}
		removeErr := miseUnregister(entry)
		after, readErr := miseEntries()
		if readErr != nil {
			return removed, readErr
		}
		removedCount := beforeCount - countMiseEntry(after, entry)
		for i := 0; i < removedCount; i++ {
			removed = append(removed, entry)
		}
		if removeErr != nil {
			return removed, fmt.Errorf("cannot unregister legacy gh-only shim directory from mise: %w", removeErr)
		}
		if removedCount <= 0 {
			return removed, fmt.Errorf("mise did not remove legacy gh-only shim entry %q; remove it manually from mise's global config", entry)
		}
	}
	return removed, nil
}

// undoLegacyMiseRemovals restores removed entries through one undo so rollback
// preserves their original relative order.
func undoLegacyMiseRemovals(removed []string) func() error {
	return func() error {
		var errs []error
		for _, entry := range removed {
			if err := miseRegister(entry); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
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
	var entries []string
	_, lookErr := miseExecutable()
	if lookErr == nil {
		if entries, err = miseEntries(); err != nil {
			return "", fmt.Errorf("cannot read mise's global config: %w", err)
		}
	}
	registered := slices.Contains(entries, dir)
	legacy := legacyMiseGHDirs(entries)

	if !enable {
		if lookErr != nil {
			return "mise not found on PATH; remove the env._.path entry for " + dir + " and any legacy relative gh-autoaccount/bin entry from mise's global config if it exists", removeMiseShim(snapshot, addUndo)
		}
		legacyRemoved, legacyErr := removeLegacyMiseEntries(legacy)
		if len(legacyRemoved) > 0 {
			addUndo(undoLegacyMiseRemovals(legacyRemoved))
		}
		if legacyErr != nil {
			return "", legacyErr
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
		return "mise not found on PATH; the gh-only shim was refreshed but mise's config was not checked; remove any legacy relative gh-autoaccount/bin entry manually once mise is available", nil
	}
	legacyRemoved, legacyErr := removeLegacyMiseEntries(legacy)
	if len(legacyRemoved) > 0 {
		addUndo(undoLegacyMiseRemovals(legacyRemoved))
	}
	if legacyErr != nil {
		return "", legacyErr
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
		doctorPrintf("     path: %s\n", resolved)
	} else {
		doctorPrintf("WARN plain gh resolves to %s, not the managed shim\n", resolved)
		doctorPrintf("     %s\n", shadowFixHint(resolved, settings))
		issues++
	}
	if runtime.GOOS == "windows" {
		// This process may predate a PATH change; new shells read the registry.
		if gh, err := shims.NewProcessGH(); err == nil && !shims.IsManagedGHShim(gh) {
			doctorPrintf("WARN new shells find %s first: Windows searches the machine PATH before the user PATH\n", gh)
			if dir, err := shims.Dir(); err == nil {
				doctorPrintf("     as administrator, run: %s\n", machinePathCommand(dir))
			}
			issues++
		}
		return true, issues
	}
	if _, err := miseExecutable(); err != nil {
		return true, issues
	}
	inMise, err := runMise("exec", "--", "sh", "-c", "command -v gh")
	switch {
	case err != nil:
		doctorPrintf("INFO cannot check gh inside mise exec: %v\n", err)
	case shims.IsManagedGHShim(inMise):
		fmt.Println("OK   gh inside mise exec resolves to the managed shim")
	default:
		doctorPrintf("WARN gh inside mise exec resolves to %s, not the managed shim\n", inMise)
		doctorPrintf("     %s\n", shadowFixHint(inMise, settings))
		issues++
	}
	return true, issues
}
