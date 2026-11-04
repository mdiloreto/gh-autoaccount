#!/usr/bin/env bash
# Offline black-box acceptance of the installed native routing interface.
set -euo pipefail
[[ $# == 1 && $1 == /* && -x $1 ]] || { printf 'Usage: %s /absolute/path/to/candidate\n' "$0" >&2; exit 2; }
candidate=$1
git_bin=$(command -v git)
root=$(mktemp -d "${TMPDIR:-/tmp}/gh-native-smoke.XXXXXX")
trap 'rm -rf -- "$root"' EXIT

env -i PATH="$PATH" HOME="$root/home" XDG_CONFIG_HOME="$root/config" \
  XDG_DATA_HOME="$root/data" XDG_STATE_HOME="$root/state" XDG_CACHE_HOME="$root/cache" \
  GH_CONFIG_DIR="$root/gh-config" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$root/gitconfig" \
  GIT_TERMINAL_PROMPT=0 GH_PROMPT_DISABLED=1 GIT_ASKPASS=/bin/false SSH_ASKPASS=/bin/false \
  TMPDIR="$root" HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 \
  ALL_PROXY=http://127.0.0.1:1 NO_PROXY= \
  bash --noprofile --norc -s -- "$candidate" "$root" "$git_bin" <<'SMOKE'
set -euo pipefail
candidate=$1 root=$2 git_bin=$3
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
mkdir -p "$HOME" "$root/bin" "$root/work/nested" "$root/emu" "$root/enterprise" "$root/missing" "$root/unpinned" "$XDG_CONFIG_HOME/gh-autoprofile"
touch "$GIT_CONFIG_GLOBAL" "$root/provider.log" "$root/direnv-deny.log" "$root/direnv-unexpected.log"
printf '%s\n' '# externally managed bash' > "$HOME/.bashrc"
printf '%s\n' '# externally managed zsh' > "$HOME/.zshrc"
ln -s "$git_bin" "$root/bin/git"
export PATH="$root/bin:$PATH" GH_AUTOPROFILE_REAL_GH="$root/bin/gh" SMOKE_ROOT="$root"
cat > "$root/bin/direnv" <<'DIRENV'
#!/usr/bin/env bash
[[ $# == 1 && $1 == version ]] && { printf '2.34.0\n'; exit 0; }
if [[ $# == 2 && $1 == deny ]]; then
  case $2 in
    "$SMOKE_ROOT/work/.envrc"|"$SMOKE_ROOT/work/nested/.envrc"|"$SMOKE_ROOT/missing/.envrc"|"$SMOKE_ROOT/emu/.envrc"|"$SMOKE_ROOT/enterprise/.envrc")
      if [[ -f $2 && ! -L $2 ]] && awk '
        $0 == "# gh-autoprofile:start" { if (state != 0) exit 1; state = 1; next }
        $0 == "# gh-autoprofile:end" { if (state != 1) exit 1; state = 2 }
        END { if (state != 2) exit 1 }
      ' "$2"; then
        printf '%s\n' "$2" >> "$SMOKE_ROOT/direnv-deny.log"
        exit 0
      fi ;;
  esac
fi
printf '%s\n' "$*" >> "$SMOKE_ROOT/direnv-unexpected.log"
exit 1
DIRENV
cat > "$root/bin/gh" <<'GH'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >> "$SMOKE_ROOT/provider.log"
case "${1:-} ${2:-}" in
  '--version ') printf 'gh version 2.86.0 (fixture)\n' ;;
  'auth status') printf 'github.com\n  ✓ Logged in to github.com account alice\n' ;;
  'auth token')
    case " $* " in
      *' --user alice '*) printf 'dummy-alice\n' ;;
      *' --user bob '*) printf 'dummy-bob\n' ;;
      *' --user alice_CORP '*) printf 'dummy-emu\n' ;;
      *' --user ent '*) printf 'dummy-enterprise\n' ;;
      *) exit 1 ;;
    esac ;;
  'auth login'|'auth switch'|'auth refresh'|'auth logout'|'auth setup-git')
    printf '%s|%s\n' "${GH_TOKEN-}" "${GITHUB_TOKEN-}" ;;
  'observe '|'observe '*)
    shift
    printf '%s|%s|%s|%s|%s|%s\n' "${GH_TOKEN-}" "${GITHUB_TOKEN-}" "${GH_ENTERPRISE_TOKEN-}" "${GITHUB_ENTERPRISE_TOKEN-}" "${GH_HOST-}" "$#"
    if (( $# )); then printf '<%s>\n' "$@"; fi ;;
  'exit73 ') exit 73 ;;
  'signal ')
    trap 'exit 42' TERM
    printf '%s\n' "$$" > "$SMOKE_ROOT/ready"
    while :; do sleep 1 & wait $! || :; done ;;
  *) printf 'unsupported fake gh call: %s\n' "$*" >&2; exit 99 ;;
esac
GH
chmod 755 "$root/bin/gh" "$root/bin/direnv"
cat > "$XDG_CONFIG_HOME/gh-autoprofile/pins.yml" <<PINS
pins:
  - user: alice
    dir: $root/work
    mode: wrapper
  - user: bob
    dir: $root/work/nested
    mode: wrapper
  - user: missing
    dir: $root/missing
    mode: wrapper
  - user: alice_CORP
    dir: $root/emu
    host: tenant.ghe.com
  - user: ent
    dir: $root/enterprise
    host: enterprise.example
    git_name: Enterprise Identity
    git_email: ent@example.invalid
    ssh_key: /fixture/ssh-key
PINS
chmod 600 "$XDG_CONFIG_HOME/gh-autoprofile/pins.yml"
"$candidate" setup --native --no-shell-rc > "$root/setup.log" 2>&1 || { cat "$root/setup.log" >&2; fail 'native setup failed'; }
shim=$HOME/.local/bin/gh
[[ -L $shim && $shim -ef $candidate ]] || fail 'native gh is not a symlink to candidate'
export PATH="$HOME/.local/bin:$PATH"
[[ $(cat "$HOME/.bashrc") == '# externally managed bash' && $(cat "$HOME/.zshrc") == '# externally managed zsh' ]] || fail 'external RC changed'
printf '%s\n' 'PASS: native setup installs candidate symlink without changing external RC'

cd "$root/work/nested"
result=$(GH_AUTOPROFILE_USER=alice GH_TOKEN=caller-one GITHUB_TOKEN=caller-two gh observe 'a b' '' '*')
[[ $result == $'dummy-bob|||||3\n<a b>\n<>\n<*>' ]] || fail "nested pin, token precedence or argument boundaries: $result"
cd "$root/work"
[[ $(GH_AUTOPROFILE_USER=bob gh observe) == 'dummy-alice|||||0' ]] || fail 'parent pin ignored'
ln -s "$root/work/nested" "$root/linked-work"
[[ $("$candidate" exec --dir "$root/linked-work" --require-pin --expect-user bob -- /bin/sh -c 'printf "%s|%s" "$GH_TOKEN" "$GITHUB_TOKEN"') == 'dummy-bob|dummy-bob' ]] || fail 'symlink pin or exec token pair incorrect'
if command -v timeout > /dev/null; then
  discovered=$(env -u GH_AUTOPROFILE_REAL_GH timeout 10 gh observe) || fail 'native gh discovery failed or recursed'
elif command -v gtimeout > /dev/null; then
  discovered=$(env -u GH_AUTOPROFILE_REAL_GH gtimeout 10 gh observe) || fail 'native gh discovery failed or recursed'
else
  discovered=$(env -u GH_AUTOPROFILE_REAL_GH gh observe) || fail 'native gh discovery failed'
fi
[[ $discovered == 'dummy-alice|||||0' ]] || fail 'native gh discovery selected the wrong provider'
for dir in work emu enterprise; do
  case $dir in
    work) native='dummy-alice|||||0'; execenv='dummy-alice|dummy-alice|||';;
    emu) native='dummy-emu||||tenant.ghe.com|0'; execenv='dummy-emu|dummy-emu|||tenant.ghe.com';;
    enterprise) native='||dummy-enterprise||enterprise.example|0'; execenv='||dummy-enterprise|dummy-enterprise|enterprise.example';;
  esac
  actual=$(cd "$root/$dir"; GH_TOKEN=stale GITHUB_TOKEN=stale GH_ENTERPRISE_TOKEN=stale GITHUB_ENTERPRISE_TOKEN=stale GH_HOST=stale gh observe)
  [[ $actual == "$native" ]] || fail "native host class $dir: $actual"
  actual=$(GH_TOKEN=stale GITHUB_TOKEN=stale GH_ENTERPRISE_TOKEN=stale GITHUB_ENTERPRISE_TOKEN=stale GH_HOST=stale "$candidate" exec --dir "$root/$dir" --require-pin -- /bin/sh -c 'printf "%s|%s|%s|%s|%s" "${GH_TOKEN-}" "${GITHUB_TOKEN-}" "${GH_ENTERPRISE_TOKEN-}" "${GITHUB_ENTERPRISE_TOKEN-}" "${GH_HOST-}"')
  [[ $actual == "$execenv" ]] || fail "exec host class $dir: $actual"
done
cd "$root/missing"
if GH_TOKEN=caller-one GITHUB_TOKEN=caller-two gh observe > "$root/missing.log" 2>&1; then fail 'missing pinned credential accepted'; fi
printf '%s\n' 'PASS: nested pins override stale marker and ambient tokens; missing credential blocks gh'

# Exercise the real helper chain; Git itself must choose the -C directory.
fill=$(printf 'protocol=https\nhost=github.com\n\n' | "$git_bin" -C "$root/work/nested" credential fill)
[[ $fill == *'password=dummy-bob'* ]] || fail 'git -C credential fill chose wrong pin'
fill=$(printf 'protocol=https\nhost=github.com\n\n' | GH_TOKEN=caller-one GITHUB_TOKEN=caller-two "$git_bin" -C "$root/work" credential fill)
[[ $fill == *'password=dummy-alice'* ]] || fail 'helper did not override inherited caller token'
if printf 'protocol=https\nhost=github.com\n\n' | GH_TOKEN=caller-one GITHUB_TOKEN=caller-two "$git_bin" -C "$root/missing" credential fill > "$root/missing-fill" 2> "$root/missing-fill.err"; then
  fail 'missing pin credential fill succeeded with ambient tokens'
fi
[[ ! -s $root/missing-fill ]] || fail 'missing pin credential fill produced credentials'
fill=$(printf 'protocol=https\nhost=enterprise.example\n\n' | "$git_bin" -C "$root/enterprise" credential fill)
[[ $fill == *'password=dummy-enterprise'* ]] || fail 'enterprise helper did not select exact host'
if printf 'protocol=https\nhost=other.example\n\n' | "$git_bin" -C "$root/enterprise" credential fill > "$root/wrong-fill" 2>/dev/null; then fail 'different host accepted'; fi
[[ ! -s $root/wrong-fill ]] || fail 'different host returned credentials'
"$candidate" exec --dir "$root/enterprise" --require-pin -- /bin/sh -c 'test "$GIT_AUTHOR_NAME" = "Enterprise Identity" && test -n "$GIT_SSH_COMMAND" && test "$GH_ENTERPRISE_TOKEN" = dummy-enterprise' || fail 'identity/SSH and host routing'
"$git_bin" -C "$root/missing" init -q
"$git_bin" -C "$root/missing" config user.name fixture
"$git_bin" -C "$root/missing" config user.email fixture@example.invalid
mkdir -p "$root/missing/.git/hooks"
cat > "$root/missing/.git/hooks/pre-commit" <<'HOOK'
#!/usr/bin/env bash
printf '%s|%s\n' "${GH_TOKEN-}" "${GITHUB_TOKEN-}" > "$SMOKE_ROOT/hook-env"
HOOK
chmod 755 "$root/missing/.git/hooks/pre-commit"
: > "$root/provider.log"
"$git_bin" -C "$root/missing" status --porcelain > /dev/null
"$git_bin" -C "$root/missing" commit -q --allow-empty --no-gpg-sign -m fixture
[[ $(cat "$root/hook-env") == '|' && ! -s $root/provider.log ]] || fail 'local git introduced credentials or provider calls'
GH_TOKEN=caller-one GITHUB_TOKEN=caller-two "$git_bin" -C "$root/missing" commit -q --allow-empty --no-gpg-sign -m inherited
[[ $(cat "$root/hook-env") == 'caller-one|caller-two' && ! -s $root/provider.log ]] || fail 'git unexpectedly sanitizes inherited environment or calls provider'
printf '%s\n' 'PASS: Git helper uses -C pin; local Git does not inject tokens (inherited tokens remain)'

cd "$root/missing"
for action in login switch refresh logout setup-git; do
  : > "$root/provider.log"
  [[ $(GH_TOKEN=caller-one GITHUB_TOKEN=caller-two gh auth "$action") == 'caller-one|caller-two' ]] || fail "auth $action environment changed"
  [[ $(wc -l < "$root/provider.log") -eq 1 ]] || fail "auth $action performed extra provider lookup"
done
: > "$root/provider.log"
[[ $(GH_AUTOPROFILE_DISABLE_SHIM=anything GH_TOKEN=caller-one GITHUB_TOKEN=caller-two gh observe) == 'caller-one|caller-two||||0' ]] || fail 'disable did not bypass routing'
[[ $(wc -l < "$root/provider.log") -eq 1 ]] || fail 'disabled routing looked up credentials'
: > "$root/provider.log"
if "$candidate" exec --dir "$root/work" --require-pin --expect-user bob -- touch "$root/child-ran" > /dev/null 2>&1; then fail 'mismatched guard accepted'; fi
if "$candidate" exec --dir "$root/unpinned" --require-pin --expect-user alice -- touch "$root/child-ran" > /dev/null 2>&1; then fail 'missing pin accepted'; fi
[[ ! -e $root/child-ran && ! -s $root/provider.log ]] || fail 'guard ran provider or child'
"$candidate" exec --dir "$root/work" --require-pin --expect-user alice -- bash -c '[[ $GH_TOKEN == dummy-alice && $GITHUB_TOKEN == dummy-alice ]]' || fail 'explicit exec did not inject token'
printf '%s\n' 'PASS: auth management/disable bypass; exec guards and explicit injection'

cd "$root/work"
status=0
gh exit73 || status=$?
[[ $status == 73 ]] || fail "exit status $status instead of 73"
gh signal &
pid=$!
for attempt in $(seq 1 200); do [[ -f $root/ready ]] && break; sleep 0.02; done
[[ -f $root/ready ]] || { kill "$pid" 2>/dev/null || :; fail 'signal fixture did not start'; }
[[ $(cat "$root/ready") == "$pid" ]] || { kill "$pid" 2>/dev/null || :; fail 'native gh did not replace its process'; }
kill -TERM "$pid"
status=0
wait "$pid" || status=$?
[[ $status == 42 ]] || fail "signal handler status $status instead of 42"
rm "$root/ready"
"$candidate" exec --dir "$root/work" --require-pin --expect-user alice -- gh signal &
pid=$!
for attempt in $(seq 1 200); do [[ -f $root/ready ]] && break; sleep 0.02; done
[[ -f $root/ready ]] || { kill "$pid" 2>/dev/null || :; fail 'exec signal fixture did not start'; }
[[ $(cat "$root/ready") == "$pid" ]] || { kill "$pid" 2>/dev/null || :; fail 'exec did not replace its process'; }
kill -TERM "$pid"
status=0
wait "$pid" || status=$?
[[ $status == 42 ]] || fail "exec signal handler status $status instead of 42"
printf '%s\n' 'PASS: exit status and native gh/exec process replacement and signal delivery'

mkdir -p "$root/attacker"
cat > "$root/attacker/gh" <<'ATTACK'
#!/bin/sh
printf attacked >> "$SMOKE_ROOT/attacked"
exit 97
ATTACK
chmod 755 "$root/attacker/gh"
helper=$XDG_CONFIG_HOME/gh-autoprofile/git-credential-helper
fill=$(cd "$root/work"; printf 'protocol=https\nhost=github.com\n\n' | PATH="$root/attacker:$PATH" GH_PATH="$root/attacker/gh" GH_AUTOPROFILE_REAL_GH="$root/attacker/gh" "$helper" get)
[[ $fill == *'password=dummy-alice'* ]] || fail 'installed helper did not pin targets'
printf '%s\n' 'PASS: installed helper resists poisoned routing'

# Setup removes what the retired direnv modes left behind and keeps user bytes.
dirs=("$root/work" "$root/work/nested" "$root/missing" "$root/emu" "$root/enterprise")
for dir in "${dirs[@]}"; do
  printf '# gh-autoprofile:start\nuse_gh_autoprofile alice\n# gh-autoprofile:end\n' > "$dir/.envrc"
done
printf '  # unmanaged fixture\n\nexport EXAMPLE=value\n' > "$root/expected-unmanaged"
cat "$root/expected-unmanaged" >> "$root/work/.envrc"
lib=$XDG_CONFIG_HOME/direnv/lib/gh-autoprofile.sh
hook=$XDG_CONFIG_HOME/gh-autoprofile/hook.sh
mkdir -p "$(dirname "$lib")"
printf '#!/usr/bin/env bash\n# gh-autoprofile direnv shell library\n' > "$lib"
printf '#!/usr/bin/env bash\n# gh-autoprofile shell hook\n' > "$hook"
: > "$root/direnv-deny.log"
: > "$root/direnv-unexpected.log"
"$candidate" setup --migrate > "$root/migrate.log" 2>&1 || { cat "$root/migrate.log" >&2; fail 'legacy cleanup migration failed'; }
[[ -L $shim && $shim -ef $candidate ]] || fail 'migrate did not keep the native proxy'
cmp -s "$root/expected-unmanaged" "$root/work/.envrc" || fail 'cleanup did not preserve unmanaged bytes'
for dir in "${dirs[@]:1}"; do
  [[ ! -e $dir/.envrc ]] || fail "cleanup left a managed-only envrc: $dir"
done
[[ $(wc -l < "$root/direnv-deny.log") -eq 5 ]] || fail 'cleanup did not deny each managed envrc once'
for dir in "${dirs[@]}"; do
  grep -Fxq "$dir/.envrc" "$root/direnv-deny.log" || fail "cleanup did not deny $dir"
done
[[ ! -s $root/direnv-unexpected.log ]] || { cat "$root/direnv-unexpected.log" >&2; fail 'unexpected direnv command, including allow'; }
[[ ! -e $lib ]] || fail 'cleanup kept the direnv library'
[[ -e $hook ]] || fail 'external-shell cleanup deleted a hook the shell manager may source'
grep -Fq "Remove the line that sources $hook" "$root/migrate.log" || fail 'external-shell hook guidance missing'
[[ $(cat "$HOME/.bashrc") == '# externally managed bash' && $(cat "$HOME/.zshrc") == '# externally managed zsh' ]] || fail 'migration changed external RC'
if "$candidate" setup --native=false > "$root/legacy.log" 2>&1; then fail 'setup --native=false should fail'; fi
grep -q 'were removed' "$root/legacy.log" || fail 'removed-mode guidance missing'
printf '%s\n' 'PASS: legacy cleanup preserves user bytes and external RC'
SMOKE
