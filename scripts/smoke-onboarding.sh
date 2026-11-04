#!/usr/bin/env bash
# Real gh extension dispatch, using only isolated synthetic credentials.
set -euo pipefail
[[ $# == 1 && $1 == /* && -x $1 ]] || { printf 'Usage: %s /absolute/path/to/candidate\n' "$0" >&2; exit 2; }
[[ $(uname -s) == Linux ]] || { echo 'Linux required' >&2; exit 2; }
candidate=$1
gh_bin=${GH_AUTOPROFILE_TEST_GH:-/usr/bin/gh}
[[ $gh_bin == /* && -x $gh_bin ]] || { echo 'absolute real gh executable required' >&2; exit 2; }
# The smoke shell runs with PATH=/usr/bin:/bin, so its tools must live there
# (Alpine keeps bash in /bin).
tool_path() {
  local dir
  for dir in /usr/bin /bin; do [[ -x $dir/$1 ]] && { printf '%s\n' "$dir/$1"; return 0; }; done
  echo "missing $1 in /usr/bin or /bin" >&2
  exit 2
}
for tool in git env jq; do tool_path "$tool" > /dev/null; done
bash_bin=$(tool_path bash)
root=$(mktemp -d "${TMPDIR:-/tmp}/gh-onboarding.XXXXXX")
trap 'rm -rf -- "$root"' EXIT
env -i PATH=/usr/bin:/bin HOME="$root/home" XDG_CONFIG_HOME="$root/config" \
  XDG_DATA_HOME="$root/data" XDG_CACHE_HOME="$root/cache" XDG_STATE_HOME="$root/state" \
  GH_CONFIG_DIR="$root/gh-config" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$root/gitconfig" \
  GIT_TERMINAL_PROMPT=0 GIT_ASKPASS=/bin/false SSH_ASKPASS=/bin/false GH_PROMPT_DISABLED=1 \
  GH_NO_UPDATE_NOTIFIER=1 GH_NO_EXTENSION_UPDATE_NOTIFIER=1 DBUS_SESSION_BUS_ADDRESS="unix:path=$root/no-bus" \
  HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 ALL_PROXY=http://127.0.0.1:1 NO_PROXY= \
  LC_ALL=C TZ=UTC TMPDIR="$root" "$bash_bin" --noprofile --norc -s -- "$candidate" "$root" "$gh_bin" <<'SMOKE'
set -euo pipefail
candidate=$1 root=$2 realgh=$3
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
version=$("$realgh" --version) || fail 'real gh --version failed'
[[ $version == 'gh version '* ]] || fail 'real gh --version lacks official gh version signature'
mkdir -p "$HOME" "$GH_CONFIG_DIR" "$root/gh-autoaccount" "$root/work/nested" "$root/outside" "$root/bin"
touch "$GIT_CONFIG_GLOBAL"
printf '# external rc\n' > "$HOME/.bashrc"
cat > "$GH_CONFIG_DIR/hosts.yml" <<'HOSTS'
github.com:
    user: alice
    oauth_token: dummy-alice
    users:
        alice:
            oauth_token: dummy-alice
        bob:
            oauth_token: dummy-bob
HOSTS
chmod 600 "$GH_CONFIG_DIR/hosts.yml"
cp "$candidate" "$root/gh-autoaccount/gh-autoaccount"
git -C "$root/gh-autoaccount" init -q
git -C "$root/gh-autoaccount" -c core.hooksPath=/dev/null -c commit.gpgSign=false -c user.name=Fixture -c user.email=fixture@example.test add gh-autoaccount
GIT_AUTHOR_DATE=2000-01-01T00:00:00Z GIT_COMMITTER_DATE=2000-01-01T00:00:00Z git -C "$root/gh-autoaccount" -c core.hooksPath=/dev/null -c commit.gpgSign=false -c user.name=Fixture -c user.email=fixture@example.test commit -qm fixture
(cd "$root/gh-autoaccount"; "$realgh" extension install .)
mkdir -p "$HOME/.local/bin" "$XDG_CONFIG_HOME/gh-autoprofile"
cat > "$HOME/.local/bin/gh" <<OLD_SHIM
#!/usr/bin/env bash
# gh-autoprofile-gh-shim
set -euo pipefail
if [[ -n "\${GH_AUTOPROFILE_DISABLE_SHIM:-}" ]]; then exec '$realgh' "\$@"; fi
GH_AUTOPROFILE_REAL_GH='$realgh' exec '$root/old-gh-autoprofile' __shim-gh "\$@"
OLD_SHIM
chmod 755 "$HOME/.local/bin/gh"
cat > "$XDG_CONFIG_HOME/gh-autoprofile/git-credential-helper" <<OLD_HELPER
#!/usr/bin/env bash
set -euo pipefail
exec '$root/old-gh-autoprofile' __git-credential "\$@"
OLD_HELPER
chmod 700 "$XDG_CONFIG_HOME/gh-autoprofile/git-credential-helper"
printf '#!/bin/sh\n[ "$#" = 1 ] && [ "$1" = version ] && exit 1\nprintf called > "%s/direnv-called"\nexit 1\n' "$root" > "$root/bin/direnv"
chmod 755 "$root/bin/direnv"
export PATH="$root/bin:/usr/bin:/bin"
"$realgh" autoaccount status --json > /dev/null
"$realgh" autoaccount setup --native --no-shell-rc > /dev/null
[[ -L $HOME/.local/bin/gh && $(cat "$HOME/.bashrc") == '# external rc' ]] || fail 'setup shim or RC'
[[ $HOME/.local/bin/gh -ef $root/gh-autoaccount/gh-autoaccount ]] || fail 'old shim did not refresh to new executable'
grep -Fq "$root/gh-autoaccount/gh-autoaccount" "$XDG_CONFIG_HOME/gh-autoprofile/git-credential-helper" || fail 'old helper did not refresh to new executable'
grep -Fq 'autoaccount exec' "$XDG_CONFIG_HOME/gh-autoprofile/hook.sh" || fail 'old-path hook did not refresh to new command'
if "$realgh" alias list | grep -q '^aa:'; then fail 'setup configured alias without opt-in'; fi
"$realgh" alias set --shell aa 'printf conflict' > /dev/null
"$realgh" alias list > "$root/aliases-before"
"$realgh" autoaccount setup --alias > /dev/null 2> "$root/alias-warning"
grep -Fq 'use gh autoaccount directly' "$root/alias-warning" || fail 'missing alias collision warning'
"$realgh" alias list > "$root/aliases-after"
cmp -s "$root/aliases-before" "$root/aliases-after" || fail 'setup overwrote existing alias'
"$realgh" alias delete aa > /dev/null
"$realgh" autoaccount setup --alias > /dev/null
"$realgh" alias list > "$root/aliases-configured"
grep -Fxq 'aa: autoaccount' "$root/aliases-configured" || fail "setup did not configure short alias; listing: $(cat "$root/aliases-configured")"
export GH_AUTOPROFILE_REAL_GH="$realgh" PATH="$HOME/.local/bin:$root/bin:/usr/bin:/bin"
gh aa status --json > /dev/null
export SMOKE_ROOT="$root"
cat > "$root/bin/observe" <<'OBSERVE'
#!/usr/bin/env bash
printf '%s' "${GH_TOKEN-}" > "$SMOKE_ROOT/observed"
printf '%s' "${GITHUB_TOKEN-}" > "$SMOKE_ROOT/observed-github"
OBSERVE
chmod 755 "$root/bin/observe"
"$realgh" alias set observe '!"$SMOKE_ROOT/bin/observe"' > /dev/null
cd "$root/outside"
gh autoaccount default alice
gh observe
[[ $(cat "$root/observed") == dummy-alice && ! -s $root/observed-github ]] || fail 'default account not routed with primary token only'
if gh autoaccount exec --require-pin -- touch "$root/ran" > /dev/null 2>&1; then fail 'default counted as pin'; fi
if gh autoaccount exec --expect-user bob -- touch "$root/ran" > /dev/null 2>&1; then fail 'mismatched expected user'; fi
[[ ! -e $root/ran ]] || fail 'guard ran child'
gh autoaccount pin bob --dir "$root/work"
gh autoaccount pin alice --dir "$root/work/nested"
[[ ! -e $root/work/.envrc && ! -e $root/work/nested/.envrc ]] || fail 'native pin created envrc'
[[ ! -e $root/direnv-called ]] || fail 'native setup or pin used unavailable direnv'
cd "$root/work"
gh observe
[[ $(cat "$root/observed") == dummy-bob && ! -s $root/observed-github ]] || fail 'pin not routed with primary token only'
gh autoaccount exec --user alice -- bash -c '[[ $GH_TOKEN == dummy-alice && $GITHUB_TOKEN == dummy-alice ]]'
cd "$root/work/nested"
gh observe
[[ $(cat "$root/observed") == dummy-alice && ! -s $root/observed-github ]] || fail 'nested pin not routed with primary token only'
fill=$(printf 'protocol=https\nhost=github.com\n\n' | git -C "$root/work" credential fill)
[[ $fill == *'password=dummy-bob'* ]] || fail 'git -C selected wrong credential'
cp "$candidate" "$root/replacement"
rm "$root/gh-autoaccount/gh-autoaccount"
ln -s "$root/replacement" "$root/gh-autoaccount/gh-autoaccount"
gh autoaccount setup --migrate > /dev/null
[[ $HOME/.local/bin/gh -ef $root/replacement ]] || fail 'native shim did not refresh to replacement target'
grep -Fq "$root/replacement" "$XDG_CONFIG_HOME/gh-autoprofile/git-credential-helper" || fail 'credential helper did not refresh to replacement target'
fill=$(printf 'protocol=https\nhost=github.com\n\n' | git -C "$root/work" credential fill)
[[ $fill == *'password=dummy-bob'* ]] || fail 'refreshed helper failed'
cd "$root/outside"
cat > "$GH_CONFIG_DIR/hosts.yml" <<'HOSTS'
github.com:
    user: bob
    oauth_token: dummy-bob
    users:
        bob:
            oauth_token: dummy-bob
HOSTS
rm "$root/observed"
if GH_TOKEN=dummy-ambient GITHUB_TOKEN=dummy-ambient gh observe > /dev/null 2>&1; then fail 'missing default credential accepted'; fi
[[ ! -e $root/observed ]] || fail 'missing default ran target'
gh autoaccount status --json > "$root/status.json"
jq -e '.selected_user == "alice" and .selection_source == "default" and .pin == null and .authentication.selected == true and .authentication.credential_available == false and .authentication.verified == false' "$root/status.json" > /dev/null || fail 'missing-default status is incorrect'
gh autoaccount doctor > /dev/null
gh autoaccount setup --migrate > /dev/null
gh aa default --unset
gh autoaccount status --json > "$root/status.json"
jq -e '.selection_source == "none" and .selected_user == "" and .authentication.selected == false' "$root/status.json" > /dev/null || fail 'unset-default status is incorrect'
gh observe
[[ -e $root/observed && ! -s $root/observed && ! -s $root/observed-github ]] || fail 'default unset retained injected token or did not run observer'
[[ $("$realgh" auth token) == dummy-bob ]] || fail 'real gh did not recover active bob credential'
printf 'PASS: real gh isolated onboarding and recovery\n'
SMOKE
