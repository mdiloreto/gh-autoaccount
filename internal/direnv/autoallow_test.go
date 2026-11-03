package direnv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoAllowSafe(t *testing.T) {
	for _, tc := range []struct {
		name               string
		dirPerm, envrcPerm os.FileMode
		wantSafe           bool
	}{
		{"0700 dir without .envrc", 0700, 0, true},
		{"0755 dir", 0755, 0, true},
		{"0770 dir", 0770, 0, false},
		{"0707 dir", 0707, 0, false},
		{"0777 dir", 0777, 0, false},
		{"0700 dir with 0664 .envrc", 0700, 0664, false},
		{"0700 dir with 0602 .envrc", 0700, 0602, false},
		{"0755 dir with 0644 .envrc", 0755, 0644, true},
		{"0700 dir with 0600 .envrc", 0700, 0600, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, tc.dirPerm); err != nil {
				t.Fatal(err)
			}
			if tc.envrcPerm != 0 {
				path := filepath.Join(dir, ".envrc")
				if err := os.WriteFile(path, []byte("# managed fixture\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, tc.envrcPerm); err != nil {
					t.Fatal(err)
				}
			}
			got, err := AutoAllowSafe(dir)
			if err != nil || got != tc.wantSafe {
				t.Fatalf("AutoAllowSafe(dir %o, .envrc %o) = %v, %v; want %v", tc.dirPerm, tc.envrcPerm, got, err, tc.wantSafe)
			}
		})
	}
	t.Run("nonexistent directory", func(t *testing.T) {
		if got, err := AutoAllowSafe(filepath.Join(t.TempDir(), "missing")); got || err == nil {
			t.Fatalf("missing directory did not fail closed: %v, %v", got, err)
		}
	})
	for _, kind := range []string{"symlink", "dangling symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".envrc")
			var err error
			switch kind {
			case "directory":
				err = os.Mkdir(path, 0700)
			case "symlink":
				err = os.Symlink(dir, path)
			default:
				err = os.Symlink(filepath.Join(dir, "missing"), path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, err := AutoAllowSafe(dir); got || err != nil {
				t.Fatalf("nonregular .envrc did not fail closed: %v, %v", got, err)
			}
		})
	}
}

func TestAllowSnapshotRealDirenv(t *testing.T) {
	bin, err := exec.LookPath("direnv")
	if err != nil {
		if os.Getenv("GH_AUTOPROFILE_REQUIRE_DIRENV_2382") == "1" {
			t.Fatalf("direnv 2.38.2 is required: %v", err)
		}
		t.Skip("direnv not installed")
	}
	version, err := exec.Command(bin, "version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "2.38.2" {
		if os.Getenv("GH_AUTOPROFILE_REQUIRE_DIRENV_2382") == "1" {
			t.Fatalf("direnv 2.38.2 is required, got %q: %v", strings.TrimSpace(string(version)), err)
		}
		t.Skip("requires real direnv 2.38.2")
	}
	for _, tc := range []string{"allowed", "real-store-0755", "data-writable", "data-symlink", "access-acl", "default-acl", "deny", "required", "legacy", "config", "config-whitelist", "symlink", "unknown-version", "data-error"} {
		t.Run(tc, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
			t.Setenv("DIRENV_CONFIG", filepath.Join(home, "direnv"))
			t.Setenv("DIRENV_REQUIRED", "")
			dir := filepath.Join(home, "repo")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".envrc")
			data := []byte("# gh-autoprofile:start\nexport SNAPSHOT_TEST=ok\n# gh-autoprofile:end\n")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			switch tc {
			case "real-store-0755", "data-writable", "data-symlink", "access-acl", "default-acl":
				seed := filepath.Join(home, "seed")
				if err := os.Mkdir(seed, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(seed, ".envrc"), []byte("# seed\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(bin, "allow", seed).CombinedOutput(); err != nil {
					t.Fatalf("seed direnv allow: %v: %s", err, out)
				}
				root := filepath.Join(home, "data", "direnv")
				allow := filepath.Join(root, "allow")
				for _, p := range []string{root, allow} {
					if err := os.Chmod(p, 0755); err != nil {
						t.Fatal(err)
					}
				}
				switch tc {
				case "data-writable":
					if err := os.Chmod(allow, 0775); err != nil {
						t.Fatal(err)
					}
				case "data-symlink":
					if err := os.Rename(allow, allow+"-actual"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(allow+"-actual", allow); err != nil {
						t.Fatal(err)
					}
				case "access-acl", "default-acl":
					setfacl, err := exec.LookPath("setfacl")
					if err != nil {
						if os.Getenv("GH_AUTOPROFILE_REQUIRE_DIRENV_2382") == "1" {
							t.Fatalf("setfacl is required: %v", err)
						}
						t.Skip("setfacl unavailable")
					}
					entry := "u:65534:r-x"
					if tc == "default-acl" {
						entry = "d:" + entry
					}
					if out, err := exec.Command(setfacl, "-m", entry, allow).CombinedOutput(); err != nil {
						if os.Getenv("GH_AUTOPROFILE_REQUIRE_DIRENV_2382") == "1" {
							t.Fatalf("ACL required: %v: %s", err, out)
						}
						t.Skipf("ACL unavailable: %v: %s", err, out)
					}
				}
			case "deny":
				cmd := exec.Command(bin, "deny", path)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("deny: %v %s", err, out)
				}
				if err := os.Chmod(filepath.Join(home, "data", "direnv"), 0700); err != nil {
					t.Fatal(err)
				}
			case "required":
				t.Setenv("DIRENV_REQUIRED", "dependency")
			case "legacy":
				if err := os.MkdirAll(filepath.Join(home, "direnv", "allow"), 0700); err != nil {
					t.Fatal(err)
				}
			case "config", "config-whitelist":
				if err := os.MkdirAll(filepath.Join(home, "direnv"), 0700); err != nil {
					t.Fatal(err)
				}
				config := "[global]\nhide_env_diff = true\n"
				if tc == "config-whitelist" {
					config = "[whitelist]\nprefix = [\"" + dir + "\"]\n"
				}
				if err := os.WriteFile(filepath.Join(home, "direnv", "direnv.toml"), []byte(config), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				link := filepath.Join(home, "link")
				if err := os.Symlink(dir, link); err != nil {
					t.Fatal(err)
				}
				dir = link
			case "unknown-version":
				fake := filepath.Join(home, "bin")
				if err := os.Mkdir(fake, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fake, "direnv"), []byte("#!/bin/sh\nprintf '9.0.0\\n'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
			case "data-error":
				if err := os.WriteFile(filepath.Join(home, "data"), []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := AllowSnapshot(dir, data)
			allowed := tc == "allowed" || tc == "real-store-0755"
			if allowed && err != nil {
				t.Fatal(err)
			}
			if !allowed && err == nil {
				t.Fatalf("%s unexpectedly auto-allowed", tc)
			}
			if (tc == "access-acl" || tc == "default-acl") && !strings.Contains(err.Error(), "ACL") {
				t.Fatalf("ACL was not the reason for refusal: %v", err)
			}
			if (tc == "config" || tc == "config-whitelist") && !strings.Contains(err.Error(), "configuration") {
				t.Fatalf("config was not the reason for refusal: %v", err)
			}
			if tc == "deny" && !strings.Contains(err.Error(), "deny exists") {
				t.Fatalf("deny was not the reason for refusal: %v", err)
			}
			if allowed || tc == "deny" {
				cmd := exec.Command(bin, "export", "json")
				cmd.Dir = dir
				out, exportErr := cmd.Output()
				if allowed && (exportErr != nil || !strings.Contains(string(out), `"SNAPSHOT_TEST": "ok"`)) {
					t.Fatalf("not loaded: %v %s", exportErr, out)
				}
				if tc == "deny" && strings.Contains(string(out), "SNAPSHOT_TEST") {
					t.Fatalf("denied file loaded: %s", out)
				}
			}
		})
	}
}
