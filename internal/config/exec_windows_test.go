//go:build windows

package config

import "testing"

func TestWindowsCommandNamesAndPaths(t *testing.T) {
	for _, path := range []string{`gh`, `gh.exe`, `GH.EXE`, `C:\Users\me\AppData\Local\gh-autoaccount\bin\gh.exe`, `C:/tools/Gh.Exe`} {
		if !IsCommandName(path, "gh") {
			t.Errorf("IsCommandName(%q, gh) = false", path)
		}
	}
	for _, path := range []string{`gh-autoaccount.exe`, `ghx.exe`, `C:\gh\git.exe`} {
		if IsCommandName(path, "gh") {
			t.Errorf("IsCommandName(%q, gh) = true", path)
		}
	}
	if ExeName("gh") != "gh.exe" {
		t.Errorf("ExeName(gh) = %q", ExeName("gh"))
	}
	if !SamePath(`C:\Users\Me\bin`, `c:\users\me\BIN`) {
		t.Error("Windows paths must compare case-insensitively")
	}
}
