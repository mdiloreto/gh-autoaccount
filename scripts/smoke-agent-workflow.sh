#!/usr/bin/env bash
# Offline acceptance check for an unreleased guard/JSON-capable candidate.
set -euo pipefail
[[ $# == 1 && -x $1 ]] || { printf 'Usage: bash %s /path/to/gh-autoaccount\n' "$0" >&2; exit 2; }
candidate=$(cd "$(dirname "$1")" && printf '%s/%s' "$PWD" "$(basename "$1")")
git_bin=$(type -P git)
go_bin=$(type -P go)
root=$(mktemp -d "${TMPDIR:-/tmp}/gh-autoprofile-smoke.XXXXXX")
trap 'rm -rf -- "$root"' EXIT

# Start clean: never pass the caller's tokens, Git config, or keyring session.
env -i PATH="$PATH" HOME="$root/home" XDG_CONFIG_HOME="$root/config" \
  XDG_DATA_HOME="$root/data" XDG_STATE_HOME="$root/state" XDG_CACHE_HOME="$root/cache" \
  GH_CONFIG_DIR="$root/gh" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
  GOENV=off GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
  TMPDIR="$root" HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 \
  bash --noprofile --norc -s -- "$candidate" "$root" "$git_bin" "$go_bin" <<'SMOKE'
set -euo pipefail
candidate=$1 root=$2 git_bin=$3 go_bin=$4
mkdir -p "$HOME" "$root/bin" "$root/unpinned" "$XDG_CONFIG_HOME/gh-autoprofile"
chmod 700 "$XDG_CONFIG_HOME/gh-autoprofile"
ln -s "$git_bin" "$root/bin/git"
export PATH="$root/bin:$PATH" GH_AUTOPROFILE_REAL_GH="$root/bin/gh"
export SMOKE_ROOT="$root" GH_TOKEN=smoke-parent-secret GITHUB_TOKEN=smoke-parent-secret
export GH_AUTOPROFILE_USER=parent GH_AUTOPROFILE_MODE=wrapper GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN=1

cat > "$root/bin/gh" <<'GH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$SMOKE_ROOT/provider.log"
case "$*" in
  'auth token --user alice --hostname github.com') printf '%s\n' smoke-alice-secret ;;
  'auth token --user bob --hostname github.com') printf '%s\n' smoke-bob-secret ;;
  'auth token --user alice_CORP --hostname tenant.ghe.com') printf '%s\n' smoke-emu-secret ;;
  'api --hostname tenant.ghe.com user --jq .login')
    case "${VERIFY_CASE:-ok}" in mismatch) printf 'wrong\n';; failure) exit 1;; *) [[ ${GH_TOKEN-} == smoke-emu-secret ]]; printf 'alice_CORP\n';; esac ;;
  'api user --jq .login')
    case "$GH_TOKEN" in
      smoke-alice-secret) user=alice other=bob ;;
      smoke-bob-secret) user=bob other=alice ;;
      *) exit 1 ;;
    esac
    [[ $GITHUB_TOKEN == "$GH_TOKEN" && $GH_AUTOPROFILE_USER == "$user" ]]
    touch "$SMOKE_ROOT/ready.$user"
    for ((attempt=0; attempt<500; attempt++)); do
      [[ -f $SMOKE_ROOT/ready.$other ]] && break
      sleep 0.01
    done
    [[ -f $SMOKE_ROOT/ready.$other ]]
    printf '%s\n' "$user"
    ;;
  *) printf '%s\n' 'Unexpected fake gh call (network/auth switching forbidden)' >&2; exit 1 ;;
esac
GH
chmod 755 "$root/bin/gh"
mkdir -p "$root/emu" "$root/broker-dir"
cat > "$root/bin/broker" <<'BROKER'
#!/usr/bin/env bash
case "$BROKER_CASE" in
  success) printf 'smoke-broker-secret\n' ;;
  empty) : ;;
  multiline) printf 'one\ntwo\n' ;;
  oversized) head -c 131072 /dev/zero | tr '\0' x ;;
  timeout) exec sleep 10 ;;
  nonzero) exit 1 ;;
esac
BROKER
chmod 755 "$root/bin/broker"
for user in alice bob; do
  git init --quiet "$root/$user"
  git -C "$root/$user" config user.name "$user"
  git -C "$root/$user" config user.email "$user@example.invalid"
  git -C "$root/$user" config core.hooksPath /dev/null
  git -C "$root/$user" commit --quiet --allow-empty --no-gpg-sign -m 'smoke fixture'
  git -C "$root/$user" worktree add --quiet --detach "$root/$user/worktree" HEAD
done
cat > "$XDG_CONFIG_HOME/gh-autoprofile/pins.yml" <<PINS
pins:
  - user: alice
    dir: $root/alice
    mode: wrapper
    git_name: alice
    git_email: alice@example.invalid
  - user: bob
    dir: $root/bob
    mode: export
    git_name: bob
    git_email: bob@example.invalid
  - user: alice_CORP
    dir: $root/emu
    host: tenant.ghe.com
  - user: broker
    dir: $root/broker-dir
    token_command: $root/bin/broker
PINS
chmod 600 "$XDG_CONFIG_HOME/gh-autoprofile/pins.yml"
cat > "$root/check-child.sh" <<'CHILD'
set -euo pipefail
[[ ! -t 0 && $PWD == "$2" ]]
[[ -f .git ]]
[[ $(gh api user --jq .login) == "$1" ]]
[[ $(git var GIT_AUTHOR_IDENT) == "$1 <$1@example.invalid> "* ]]
[[ $(git var GIT_COMMITTER_IDENT) == "$1 <$1@example.invalid> "* ]]
CHILD

"$candidate" exec --dir "$root/alice/worktree" --require-pin --expect-user ALICE -- bash "$root/check-child.sh" alice "$root/alice/worktree" </dev/null &
alice_pid=$!
GH_AUTOPROFILE_USER=bob "$candidate" exec --dir "$root/bob/worktree" --require-pin --expect-user bob -- bash "$root/check-child.sh" bob "$root/bob/worktree" </dev/null &
bob_pid=$!
failed=0
wait "$alice_pid" || failed=1
wait "$bob_pid" || failed=1
[[ $failed == 0 ]]
printf '%s\n' 'PASS: concurrent linked worktrees, inherited profiles, and distinct Git identities (no TTY)'

if "$candidate" exec --dir "$root/alice/worktree" --require-pin --expect-user bob -- touch "$root/child-ran" >"$root/guard.log" 2>&1; then
  printf '%s\n' 'FAIL: mismatched profile accepted' >&2; exit 1
fi
if "$candidate" exec --dir "$root/unpinned" --user alice --require-pin --expect-user alice -- touch "$root/child-ran" >>"$root/guard.log" 2>&1; then
  printf '%s\n' 'FAIL: unpinned override accepted' >&2; exit 1
fi
[[ ! -e $root/child-ran ]]
: > "$root/provider.log"
if "$candidate" exec --dir "$root/alice" --user bob --require-pin --verify-identity -- touch "$root/child-ran" >>"$root/guard.log" 2>&1; then exit 1; fi
[[ ! -s $root/provider.log && ! -e $root/child-ran ]]
for verify in ok mismatch failure; do
  if VERIFY_CASE=$verify "$candidate" exec --dir "$root/emu" --require-pin --expect-user alice_CORP --verify-identity -- touch "$root/child-ran" >>"$root/guard.log" 2>&1; then
    [[ $verify == ok && -e $root/child-ran ]]
    rm "$root/child-ran"
  else
    [[ $verify != ok && ! -e $root/child-ran ]]
  fi
done
for entry in 'none|||smoke-bob-secret' 'match|bob|github.com|smoke-parent-secret' 'user|bob||smoke-bob-secret' 'host||github.com|smoke-bob-secret' 'wrong-user|alice|github.com|smoke-bob-secret' 'wrong-host|bob|other.example|smoke-bob-secret'; do
  IFS='|' read -r label marker host expected <<< "$entry"
  result=$(GH_AUTOPROFILE_USER="$marker" GH_AUTOPROFILE_HOST="$host" "$candidate" exec --dir "$root/bob" --require-pin -- /bin/sh -c 'printf %s "$GH_TOKEN"')
  [[ $result == "$expected" ]] || { printf 'FAIL: ambient marker %s\n' "$label" >&2; exit 1; }
done
for broker_case in success empty multiline oversized timeout nonzero; do
  if BROKER_CASE=$broker_case "$candidate" exec --dir "$root/broker-dir" --require-pin -- /bin/sh -c 'printf %s "$GH_TOKEN" > "$SMOKE_ROOT/broker-child"' >"$root/broker.log" 2>&1; then
    [[ $broker_case == success && $(cat "$root/broker-child") == smoke-broker-secret ]]
    rm "$root/broker-child"
  else
    [[ $broker_case != success && ! -e $root/broker-child ]]
  fi
done
printf '%s\n' 'PASS: host verification, ambient markers and broker outcomes'
printf '%s\n' 'PASS: guards reject before child execution, including --user without a pin'

for user in alice bob; do
  "$candidate" status --dir "$root/$user/worktree" --json > "$root/$user.json"
done
"$candidate" list --json > "$root/list.json"
cat > "$root/check-json.go" <<'GO'
package main

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "strings"
)

func main() {
    root := os.Args[1]
    for _, name := range []string{"alice", "bob", "list"} {
        data, err := os.ReadFile(filepath.Join(root, name+".json"))
        if err != nil { panic(err) }
        if strings.Contains(string(data), "smoke-") { panic("credential in JSON") }
        var doc struct {
            Version int `json:"schema_version"`
            Directory string `json:"directory"`
            Pin *struct { User string; Directory string; Host string; Broker bool `json:"token_command_configured"` } `json:"pin"`
            Pins []struct { User string; Host string; Broker bool `json:"token_command_configured"` } `json:"pins"`
            Environment struct { Host string `json:"profile_host"`; GH bool `json:"gh_token_present"`; Github bool `json:"github_token_present"`; Enterprise bool `json:"gh_enterprise_token_present"`; GithubEnterprise bool `json:"github_enterprise_token_present"` } `json:"environment"`
            Auth struct {
                Selected bool `json:"selected"`
                Available bool `json:"credential_available"`
                Verified *bool `json:"verified"`
                Source string `json:"source"`
            } `json:"authentication"`
            Git struct { Directory string `json:"git_dir"`; Author string `json:"author_ident"`; Committer string `json:"committer_ident"` } `json:"git"`
        }
        if err := json.Unmarshal(data, &doc); err != nil { panic(err) }
        if doc.Version != 1 { panic("unexpected schema version") }
        if name == "list" {
            if len(doc.Pins) != 4 || doc.Pins[0].User != "alice" || doc.Pins[1].User != "bob" || doc.Pins[0].Host != "github.com" || doc.Pins[2].Host != "tenant.ghe.com" || !doc.Pins[3].Broker { panic("unexpected pins") }
            continue
        }
        if doc.Directory != filepath.Join(root, name, "worktree") || doc.Pin == nil || doc.Pin.User != name || doc.Pin.Directory != filepath.Join(root, name) { panic("wrong directory/inherited profile") }
        if doc.Pin.Host != "github.com" || doc.Pin.Broker || doc.Environment.Host != "" || !doc.Environment.GH || !doc.Environment.Github || doc.Environment.Enterprise || doc.Environment.GithubEnterprise { panic("wrong host or token presence") }
        if !strings.HasPrefix(doc.Git.Directory, filepath.Join(root, name, ".git", "worktrees")+string(os.PathSeparator)) { panic("wrong linked worktree Git directory") }
        if !doc.Auth.Selected || !doc.Auth.Available || doc.Auth.Verified == nil || *doc.Auth.Verified || doc.Auth.Source != "keyring" { panic("unexpected local auth diagnostics") }
        identity := fmt.Sprintf("%s <%s@example.invalid> ", name, name)
        if !strings.HasPrefix(doc.Git.Author, identity) || !strings.HasPrefix(doc.Git.Committer, identity) { panic("wrong Git identity") }
    }
}
GO
"$go_bin" run "$root/check-json.go" "$root"
if grep -q 'smoke-' "$root/guard.log"; then
  printf '%s\n' 'FAIL: credential in guard error' >&2; exit 1
fi
[[ $GH_TOKEN == smoke-parent-secret && $GITHUB_TOKEN == smoke-parent-secret ]]
[[ $GH_AUTOPROFILE_USER == parent && $GH_AUTOPROFILE_MODE == wrapper ]]
printf '%s\n' 'PASS: schema_version 1 JSON, no credential strings, unchanged parent environment'
SMOKE
