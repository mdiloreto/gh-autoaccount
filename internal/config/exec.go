package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// IsExecutable reports whether info describes a file the OS will run. Windows
// has no execute permission bit; a regular file there runs by its extension.
func IsExecutable(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0111 != 0
}

// ExeName returns the file name the OS uses for a command, adding .exe on Windows.
func ExeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// IsCommandName reports whether a path's base name is the given command,
// ignoring a trailing .exe and case on Windows, where both are optional.
func IsCommandName(path, name string) bool {
	base := filepath.Base(path)
	if runtime.GOOS != "windows" {
		return base == name
	}
	return strings.EqualFold(strings.TrimSuffix(strings.ToLower(base), ".exe"), name)
}

// SamePath compares two cleaned paths the way the OS does: case-insensitively
// on Windows, exactly elsewhere.
func SamePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
