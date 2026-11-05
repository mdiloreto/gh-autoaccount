#!/usr/bin/env bash
set -euo pipefail
[[ $# == 0 || ( $# == 1 && $1 == --check ) ]] || exit 2
mkdir -p "$DEMO_ROOT/provider" "$XDG_DATA_HOME/gh/extensions/gh-autoaccount" "$HOME/personal" "$HOME/work/src"
ln -s "$DEMO_REPO/demo/gh" "$DEMO_ROOT/provider/gh"
ln -s "$DEMO_BINARY" "$XDG_DATA_HOME/gh/extensions/gh-autoaccount/gh-autoaccount"
printf 'alice\n' > "$DEMO_ROOT/global-account"
export PATH="$DEMO_ROOT/provider:$PATH" GH_AUTOPROFILE_REAL_GH="$DEMO_ROOT/provider/gh"
if [[ $# == 0 ]]; then
  cd "$HOME"
  demo_scene() {
    printf '\033[H\033[2J\033[1;36m  gh-autoaccount\033[0m  /  %s\n' "$1"
    printf '  %s\n' "$2"
    printf '\033[90m  Offline demo | simulated accounts and API\033[0m\n\n'
  }
  export -f demo_scene
  demo_scene 'Your projects. Your accounts.' 'One setup. Account selection on every command.'
  export PS1='\[\e[36m\]demo:\w$ \[\e[0m\]'
  exec bash --noprofile --norc -i
fi
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
gh autoaccount setup --no-shell-rc > "$DEMO_ROOT/setup.out"
export PATH="$HOME/.local/bin:$PATH"
[[ $(command -v gh) == "$HOME/.local/bin/gh" ]] || fail 'native shim not active'
gh autoaccount default alice > "$DEMO_ROOT/default.out"
gh autoaccount pin bob --dir "$HOME/work" > "$DEMO_ROOT/pin.out"
for spec in personal:alice:default work/src:bob:pin personal:alice:default; do
  IFS=: read -r dir expected source <<< "$spec"
  cd "$HOME/$dir"
  [[ $(gh api /user --jq .login) == "$expected" ]] || fail 'unexpected API identity'
  gh autoaccount status --json > "$DEMO_ROOT/status.json"
  python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); assert s["selected_user"] == sys.argv[2] and s["selection_source"] == sys.argv[3] and s["authentication"]["verified"] is False' "$DEMO_ROOT/status.json" "$expected" "$source" || fail 'unexpected status'
done
cd "$HOME/work/src"
[[ $(gh autoaccount exec --expect-user bob -- gh api /user --jq .login) == bob ]] || fail 'guarded command used wrong account'
if gh autoaccount exec --expect-user alice -- gh api /user --jq .login > "$DEMO_ROOT/guard.out" 2>&1; then fail 'mismatched account guard accepted'; fi
[[ ! -e $HOME/work/.envrc && ! -e $HOME/personal/.envrc ]] || fail 'unexpected envrc'
[[ $(cat "$DEMO_ROOT/global-account") == alice ]] || fail 'global account changed'
if gh auth switch > /dev/null 2>&1; then fail 'auth switch accepted'; fi
if "$DEMO_ROOT/provider/gh" unsupported > /dev/null 2>&1; then fail 'unknown operation accepted'; fi
if GH_TOKEN=wrong "$DEMO_ROOT/provider/gh" api user > /dev/null 2>&1; then fail 'unknown credential accepted'; fi
if GH_TOKEN=ambient "$DEMO_ROOT/provider/gh" auth token --user alice --hostname github.com > /dev/null 2>&1; then fail 'ambient credential accepted'; fi
if GH_HOST=wrong "$DEMO_ROOT/provider/gh" api user > /dev/null 2>&1; then fail 'wrong host accepted'; fi
[[ $(cat "$DEMO_ROOT/global-account") == alice ]] || fail 'global account changed after rejected switch'
if grep -q 'fixture-.*-credential' "$DEMO_ROOT/setup.out" "$DEMO_ROOT/default.out" "$DEMO_ROOT/pin.out" "$DEMO_ROOT/status.json"; then fail 'credential in output'; fi
printf 'PASS: offline setup, default and pin routing (alice/bob/alice)\n'
