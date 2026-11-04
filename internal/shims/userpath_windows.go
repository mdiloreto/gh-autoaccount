//go:build windows

package shims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	userEnvKey    = `Environment`
	machineEnvKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`
)

func readPathValue(root registry.Key, key string) (value string, valueType uint32, exists bool, err error) {
	k, err := registry.OpenKey(root, key, registry.QUERY_VALUE)
	if err != nil {
		return "", 0, false, err
	}
	defer k.Close()
	value, valueType, err = k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.EXPAND_SZ, false, nil
	}
	return value, valueType, err == nil, err
}

// pathEntries splits a Path value into its raw entries. A semicolon inside
// double quotes belongs to the entry, as Windows treats it. Entries keep their
// exact text so rewriting the value never changes the ones it leaves alone.
func pathEntries(value string) []string {
	var entries []string
	start, quoted := 0, false
	for i, r := range value {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ';' && !quoted:
			entries = append(entries, value[start:i])
			start = i + 1
		}
	}
	entries = append(entries, value[start:])
	var kept []string
	for _, entry := range entries {
		if strings.TrimSpace(entry) != "" {
			kept = append(kept, entry)
		}
	}
	return kept
}

// entryDir returns the directory a raw Path entry names: quotes removed and
// %VARIABLES% expanded.
func entryDir(entry string) string {
	entry = strings.ReplaceAll(strings.TrimSpace(entry), `"`, "")
	if expanded, err := registry.ExpandString(entry); err == nil {
		entry = expanded
	}
	return entry
}

// samePathEntry compares a Path entry with a directory the way Windows does:
// case-insensitively, after expansion.
func samePathEntry(entry, dir string) bool {
	return strings.EqualFold(filepath.Clean(entryDir(entry)), filepath.Clean(dir))
}

func userPathHas(dir string) bool {
	value, _, _, err := readPathValue(registry.CURRENT_USER, userEnvKey)
	if err != nil {
		return false
	}
	for _, entry := range pathEntries(value) {
		if samePathEntry(entry, dir) {
			return true
		}
	}
	return false
}

// PrependUserPath makes dir the first entry of the persistent user PATH and
// returns a function that restores the previous value. It reports false when
// dir already comes first.
func PrependUserPath(dir string) (func() error, bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, false, err
	}
	defer k.Close()
	old, valueType, err := k.GetStringValue("Path")
	existed := err == nil
	if errors.Is(err, registry.ErrNotExist) {
		old, valueType = "", registry.EXPAND_SZ
	} else if err != nil {
		return nil, false, err
	}
	entries := pathEntries(old)
	if len(entries) > 0 && samePathEntry(entries[0], dir) {
		return func() error { return nil }, false, nil
	}
	next := []string{dir}
	for _, entry := range entries {
		if !samePathEntry(entry, dir) {
			next = append(next, entry)
		}
	}
	if err := setPathValue(k, valueType, strings.Join(next, ";")); err != nil {
		return nil, false, err
	}
	broadcastEnvironmentChange()
	restore := func() error {
		k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		if existed {
			err = setPathValue(k, valueType, old)
		} else {
			err = k.DeleteValue("Path")
		}
		broadcastEnvironmentChange()
		return err
	}
	return restore, true, nil
}

func setPathValue(k registry.Key, valueType uint32, value string) error {
	if valueType == registry.SZ {
		return k.SetStringValue("Path", value)
	}
	return k.SetExpandStringValue("Path", value)
}

var procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// broadcastEnvironmentChange tells Explorer and other listeners to reload the
// environment, so shells started from them see the new PATH.
func broadcastEnvironmentChange() {
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001a, 0x0002
	name, err := windows.UTF16PtrFromString("Environment")
	if err != nil {
		return
	}
	var result uintptr
	_, _, _ = procSendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(name)), smtoAbortIfHung, 5000, uintptr(unsafe.Pointer(&result)))
}

// NewProcessGH returns the gh.exe a newly started process finds. Windows builds
// a new process PATH from the machine Path followed by the user Path, so a gh on
// the machine Path shadows anything the user Path adds.
func NewProcessGH() (string, error) {
	machine, _, _, machineErr := readPathValue(registry.LOCAL_MACHINE, machineEnvKey)
	user, _, _, userErr := readPathValue(registry.CURRENT_USER, userEnvKey)
	if machineErr != nil && userErr != nil {
		return "", errors.Join(machineErr, userErr)
	}
	for _, entry := range append(pathEntries(machine), pathEntries(user)...) {
		candidate := filepath.Join(entryDir(entry), "gh.exe")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no gh.exe on the machine or user PATH")
}
