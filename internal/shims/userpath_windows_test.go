//go:build windows

package shims

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// TestWindowsUserPathPrependAndRestore edits the real HKCU Path, so it restores
// the original value even when an assertion fails. CI runners are disposable.
func TestWindowsUserPathPrependAndRestore(t *testing.T) {
	before, _, existed, err := readPathValue(registry.CURRENT_USER, userEnvKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.SET_VALUE)
		if err != nil {
			t.Error(err)
			return
		}
		defer k.Close()
		if existed {
			err = k.SetExpandStringValue("Path", before)
		} else if err = k.DeleteValue("Path"); errors.Is(err, registry.ErrNotExist) {
			err = nil
		}
		if err != nil {
			t.Error(err)
		}
	})
	dir := `C:\gh-autoaccount-test\bin`
	restore, changed, err := PrependUserPath(dir)
	if err != nil || !changed {
		t.Fatalf("prepend: changed=%v err=%v", changed, err)
	}
	if !userPathHas(strings.ToUpper(dir)) {
		t.Fatal("user PATH lookup must ignore case")
	}
	value, _, _, _ := readPathValue(registry.CURRENT_USER, userEnvKey)
	if first := pathEntries(value)[0]; first != dir {
		t.Fatalf("first user PATH entry %q", first)
	}
	if _, changed, err := PrependUserPath(dir); err != nil || changed {
		t.Fatalf("second prepend must be a no-op: changed=%v err=%v", changed, err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if after, _, _, _ := readPathValue(registry.CURRENT_USER, userEnvKey); after != before {
		t.Fatalf("restore left %q, want %q", after, before)
	}
}

func TestWindowsPathEntriesKeepQuotedSemicolons(t *testing.T) {
	got := pathEntries(`"C:\SDK; tools\bin";C:\a;;%SystemRoot%\x; `)
	want := []string{`"C:\SDK; tools\bin"`, `C:\a`, `%SystemRoot%\x`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("pathEntries = %q, want %q", got, want)
	}
	if !samePathEntry(`"C:\Program Files\GitHub CLI"`, `c:\program files\github cli\`) {
		t.Fatal("a quoted entry must match its directory")
	}
}
