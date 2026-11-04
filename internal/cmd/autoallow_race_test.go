package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mdiloreto/gh-autoaccount/internal/config"
	direnvlib "github.com/mdiloreto/gh-autoaccount/internal/direnv"
)

func TestAutoAllowSameUIDRace(t *testing.T) {
	realDirenv, err := exec.LookPath("direnv")
	if err != nil {
		t.Skip("real direnv unavailable")
	}
	for _, surface := range []string{"pin", "setup-migrate"} {
		t.Run(surface, func(t *testing.T) {
			home := onboardingHome(t)
			t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
			t.Setenv("REAL_DIRENV", realDirenv)
			dir := filepath.Join(home, "checkout")
			t.Setenv("RACE_DIR", dir)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			wrapper := `#!/bin/sh
if [ "$1" = version ] && [ -f "$RACE_DIR/.envrc" ] && grep -q '^# gh-autoprofile:start' "$RACE_DIR/.envrc"; then
  cp "$RACE_DIR/.envrc" "$RACE_DIR/inspected"
  printf 'export AUTOALLOW_RACE_PROBE=uninspected\n' > "$RACE_DIR/.envrc.swap"
  mv "$RACE_DIR/.envrc.swap" "$RACE_DIR/.envrc"
fi
exec "$REAL_DIRENV" "$@"
`
			if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte(wrapper), 0700); err != nil {
				t.Fatal(err)
			}
			setup := NewSetupCmd()
			setup.SetArgs([]string{"--no-shell-rc", "--native=false"})
			captureOnboarding(t, setup.Execute)
			if surface == "pin" {
				pin := NewPinCmd()
				pin.SetArgs([]string{"alice", "--dir", dir})
				captureOnboarding(t, pin.Execute)
			} else {
				registry := &config.PinRegistry{Pins: []config.Pin{{User: "alice", Dir: dir, Mode: config.ModeWrapper}}}
				if err := config.SavePins(registry); err != nil {
					t.Fatal(err)
				}
				setup := NewSetupCmd()
				setup.SetArgs([]string{"--migrate", "--no-shell-rc"})
				captureOnboarding(t, setup.Execute)
			}
			contents, err := os.ReadFile(filepath.Join(dir, ".envrc"))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(realDirenv, "export", "json")
			cmd.Dir = dir
			out, err := cmd.Output()
			if err == nil {
				t.Fatalf("real direnv unexpectedly allowed swapped file: %s", out)
			}
			var exported map[string]any
			if err := json.Unmarshal(out, &exported); err != nil {
				t.Fatal(err)
			}
			fmt.Printf("%s stored .envrc: %s", surface, contents)
			fmt.Printf("%s real direnv export AUTOALLOW_RACE_PROBE=%v\n", surface, exported["AUTOALLOW_RACE_PROBE"])
			if string(contents) != "export AUTOALLOW_RACE_PROBE=uninspected\n" || exported["AUTOALLOW_RACE_PROBE"] != nil {
				t.Fatalf("replacement was not made or was authorized")
			}
			inspected, err := os.ReadFile(filepath.Join(dir, "inspected"))
			if err != nil {
				t.Fatalf("swap hook did not run: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".envrc"), inspected, 0600); err != nil {
				t.Fatal(err)
			}
			cmd = exec.Command(realDirenv, "status", "--json")
			cmd.Dir = dir
			out, err = cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var status struct {
				State struct {
					Found struct {
						Allowed int `json:"allowed"`
					} `json:"foundRC"`
				} `json:"state"`
			}
			if err := json.Unmarshal(out, &status); err != nil {
				t.Fatal(err)
			}
			if status.State.Found.Allowed != 0 {
				t.Fatalf("inspected content not allowed: %s", out)
			}
		})
	}
}

func TestAutoAllowPreservesRealDirenvDeny(t *testing.T) {
	realDirenv, err := exec.LookPath("direnv")
	if err != nil {
		t.Skip("real direnv unavailable")
	}
	for _, surface := range []string{"pin", "setup-migrate"} {
		t.Run(surface, func(t *testing.T) {
			home := onboardingHome(t)
			dir := filepath.Join(home, "checkout")
			if err := direnvlib.InstallShellLib("/bin/sh", "/bin/sh"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".envrc")
			if err := os.WriteFile(path, []byte("# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\n"), 0600); err != nil {
				t.Fatal(err)
			}
			deny := exec.Command(realDirenv, "deny", path)
			if out, err := deny.CombinedOutput(); err != nil {
				t.Fatalf("deny: %v: %s", err, out)
			}
			if err := os.Chmod(filepath.Join(home, "data", "direnv"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "bin", "direnv"), []byte("#!/bin/sh\nexec \"$REAL_DIRENV\" \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("REAL_DIRENV", realDirenv)
			if surface == "pin" {
				cmd := NewPinCmd()
				cmd.SetArgs([]string{"alice", "--dir", dir})
				captureOnboarding(t, cmd.Execute)
			} else {
				if err := config.SavePins(&config.PinRegistry{Pins: []config.Pin{{User: "alice", Dir: dir}}}); err != nil {
					t.Fatal(err)
				}
				cmd := NewSetupCmd()
				cmd.SetArgs([]string{"--migrate", "--no-shell-rc"})
				captureOnboarding(t, cmd.Execute)
			}
			cmd := exec.Command(realDirenv, "status", "--json")
			cmd.Dir = dir
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			var status struct {
				State struct {
					Found struct {
						Allowed int `json:"allowed"`
					} `json:"foundRC"`
				} `json:"state"`
			}
			if err := json.Unmarshal(out, &status); err != nil {
				t.Fatal(err)
			}
			if status.State.Found.Allowed != 2 {
				t.Fatalf("deny not preserved: %s", out)
			}
		})
	}
}
