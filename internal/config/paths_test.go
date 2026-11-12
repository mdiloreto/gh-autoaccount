package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func requireTestHome(t *testing.T, home string) {
	t.Helper()
	got, err := os.UserHomeDir()
	if err != nil || got != home {
		t.Skipf("os.UserHomeDir does not use HOME or USERPROFILE: got %q, %v", got, err)
	}
}

func TestNormalizeEnvRelativeRoots(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	requireTestHome(t, home)
	for name, value := range map[string]string{
		"XDG_CONFIG_HOME":   ".",
		"XDG_DATA_HOME":     "relative",
		"XDG_STATE_HOME":    "relative-state",
		"XDG_CACHE_HOME":    "relative-cache",
		"GH_CONFIG_DIR":     "relative-gh",
		"GIT_CONFIG_GLOBAL": "relative-gitconfig",
		"GIT_CONFIG_SYSTEM": "relative-system-gitconfig",
		"DIRENV_CONFIG":     "relative-direnv",
		"ZDOTDIR":           "relative-zdotdir",
	} {
		t.Setenv(name, value)
	}
	if err := NormalizeEnv(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if value, set := os.LookupEnv(name); set {
			t.Errorf("%s = %q; want unset", name, value)
		}
	}
	for name, want := range map[string]string{
		"HOME":              home,
		"USERPROFILE":       home,
		"DIRENV_CONFIG":     "relative-direnv",
		"ZDOTDIR":           "relative-zdotdir",
	} {
		if value := os.Getenv(name); value != want {
			t.Errorf("%s = %q; want %q", name, value, want)
		}
	}
	if got, err := ConfigHome(); err != nil || got != filepath.Join(home, ".config") || !filepath.IsAbs(got) {
		t.Errorf("ConfigHome() = %q, %v; want absolute home fallback", got, err)
	}
	if got, err := DataHome(); err != nil || got != filepath.Join(home, ".local", "share") || !filepath.IsAbs(got) {
		t.Errorf("DataHome() = %q, %v; want absolute home fallback", got, err)
	}
}

func TestNormalizeEnvRejectsRelativeHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows replaces an invalid HOME from USERPROFILE")
	}
	t.Setenv("HOME", ".")
	t.Setenv("USERPROFILE", t.TempDir())
	if err := NormalizeEnv(); err == nil || !strings.Contains(err.Error(), "HOME must be an absolute path") {
		t.Fatalf("NormalizeEnv() = %v; want an absolute HOME error", err)
	}
	if home, set := os.LookupEnv("HOME"); !set || home != "." {
		t.Fatalf("HOME = %q, set=%t; want the original relative value", home, set)
	}
}

func TestNormalizeEnvRejectsMissingHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows requires HOME or USERPROFILE")
	}
	t.Setenv("HOME", "")
	os.Unsetenv("HOME")
	if err := NormalizeEnv(); err == nil || !strings.Contains(err.Error(), "HOME is not set") {
		t.Fatalf("NormalizeEnv() = %v; want a missing HOME error", err)
	}
}

func TestNormalizeEnvWindowsHome(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific HOME normalization")
	}
	profile := t.TempDir()
	for _, tc := range []struct {
		name, home, profile, want, wantProfile string
		wantErr                                bool
	}{
		{name: "relative home", home: ".", profile: profile, want: profile, wantProfile: profile},
		{name: "empty home", home: "", profile: profile, want: profile, wantProfile: profile},
		{name: "absolute home", home: profile, profile: "relative", want: profile, wantProfile: profile},
		{name: "both relative", home: ".", profile: "relative", wantErr: true},
		{name: "both empty", home: "", profile: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("USERPROFILE", tc.profile)
			t.Setenv("HOME", tc.home)
			for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR"} {
				t.Setenv(name, "")
			}
			err := NormalizeEnv()
			if tc.wantErr {
				if err == nil {
					t.Fatal("NormalizeEnv() = nil; want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := os.Getenv("HOME"); got != tc.want {
				t.Fatalf("HOME = %q; want %q", got, tc.want)
			}
			if got := os.Getenv("USERPROFILE"); got != tc.wantProfile {
				t.Fatalf("USERPROFILE = %q; want %q", got, tc.wantProfile)
			}
		})
	}
}

func TestNormalizeEnvWindowsRelativeAppData(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific AppData normalization")
	}
	home := t.TempDir()
	setTestHome(t, home)
	for name, value := range map[string]string{
		"AppData":         ".",
		"LocalAppData":    "relative-local",
		"XDG_CONFIG_HOME": home,
		"XDG_DATA_HOME":   home,
		"XDG_STATE_HOME":  home,
		"XDG_CACHE_HOME":  home,
		"GH_CONFIG_DIR":   home,
	} {
		t.Setenv(name, value)
	}
	if err := NormalizeEnv(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AppData", "LocalAppData"} {
		if value, set := os.LookupEnv(name); set {
			t.Errorf("%s = %q; want unset", name, value)
		}
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "HOME", "USERPROFILE"} {
		if value := os.Getenv(name); value != home {
			t.Errorf("%s = %q; want %q", name, value, home)
		}
	}
}

func TestNormalizeEnvPreservesAbsoluteRoots(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		t.Setenv(name, home)
	}
	for i := 0; i < 2; i++ {
		if err := NormalizeEnv(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM"} {
		if value, set := os.LookupEnv(name); !set || value != home {
			t.Errorf("%s = %q, set=%t; want %q", name, value, set, home)
		}
	}
}

func TestHomeDir(t *testing.T) {
	t.Run("absolute home", func(t *testing.T) {
		home := t.TempDir()
		setTestHome(t, home)
		requireTestHome(t, home)
		if got, err := HomeDir(); err != nil || got != home {
			t.Fatalf("HomeDir() = %q, %v; want %q", got, err, home)
		}
	})
	for _, home := range []string{"relative-home", "relative\nhome"} {
		t.Run(strconv.Quote(home), func(t *testing.T) {
			setTestHome(t, home)
			requireTestHome(t, home)
			got, err := HomeDir()
			if err == nil || got != "" || !strings.Contains(err.Error(), strconv.Quote(home)) {
				t.Fatalf("HomeDir() = %q, %v; want an error quoting %q", got, err, home)
			}
		})
	}
	t.Run("missing home", func(t *testing.T) {
		setTestHome(t, "")
		if _, err := os.UserHomeDir(); err == nil {
			t.Skip("os.UserHomeDir does not report a missing HOME or USERPROFILE")
		}
		got, err := HomeDir()
		if err == nil || got != "" || !strings.Contains(err.Error(), "cannot determine home directory:") {
			t.Fatalf("HomeDir() = %q, %v; want a wrapped home-directory error", got, err)
		}
	})
}

func TestXDGHome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		variable string
		lookup   func() (string, error)
		fallback []string
	}{
		{name: "config", variable: "XDG_CONFIG_HOME", lookup: ConfigHome, fallback: []string{".config"}},
		{name: "data", variable: "XDG_DATA_HOME", lookup: DataHome, fallback: []string{".local", "share"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, absolute := t.TempDir(), t.TempDir()
			setTestHome(t, home)
			wantFallback := filepath.Join(append([]string{home}, tc.fallback...)...)
			for _, path := range []struct {
				name, value, want string
				needsHome         bool
			}{
				{name: "empty", want: wantFallback, needsHome: true},
				{name: "current directory", value: ".", want: wantFallback, needsHome: true},
				{name: "relative directory", value: filepath.Join("relative", "directory"), want: wantFallback, needsHome: true},
				{name: "absolute directory", value: absolute, want: absolute},
			} {
				t.Run(path.name, func(t *testing.T) {
					t.Setenv(tc.variable, path.value)
					if path.needsHome {
						requireTestHome(t, home)
					}
					got, err := tc.lookup()
					if err != nil || got != path.want {
						t.Fatalf("%s() = %q, %v; want %q", tc.name, got, err, path.want)
					}
				})
			}
			t.Run("absolute XDG with relative home", func(t *testing.T) {
				setTestHome(t, "relative-home")
				t.Setenv(tc.variable, absolute)
				got, err := tc.lookup()
				if err != nil || got != absolute {
					t.Fatalf("%s() = %q, %v; want %q", tc.name, got, err, absolute)
				}
			})
			t.Run("relative XDG with relative home", func(t *testing.T) {
				setTestHome(t, "relative-home")
				requireTestHome(t, "relative-home")
				t.Setenv(tc.variable, ".")
				if got, err := tc.lookup(); err == nil || got != "" {
					t.Fatalf("%s() = %q, %v; want a home-directory error", tc.name, got, err)
				}
			})
		})
	}
}

func TestConfigDirIgnoresRelativeXDGConfigHome(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	setTestHome(t, home)
	requireTestHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", ".")
	t.Chdir(cwd)
	got, err := ConfigDir()
	want := filepath.Join(home, ".config", "gh-autoprofile")
	if err != nil || got != want || !filepath.IsAbs(got) {
		t.Fatalf("ConfigDir() = %q, %v; want absolute %q", got, err, want)
	}
	if rel, err := filepath.Rel(cwd, got); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("ConfigDir() = %q is inside the working directory %q", got, cwd)
	}
}
