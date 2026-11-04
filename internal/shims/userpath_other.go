//go:build !windows

package shims

import "errors"

var errWindowsOnly = errors.New("the persistent user PATH is managed this way only on Windows")

func userPathHas(string) bool { return false }

// PrependUserPath is Windows-only; other platforms use the shell RC PATH block.
func PrependUserPath(string) (func() error, bool, error) { return nil, false, errWindowsOnly }

// NewProcessGH is Windows-only; other platforms resolve gh from the shell PATH.
func NewProcessGH() (string, error) { return "", errWindowsOnly }
