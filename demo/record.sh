#!/usr/bin/env bash
set -euo pipefail
[[ $# == 0 || ( $# == 1 && $1 == --check ) ]] || { printf 'Usage: bash demo/record.sh [--check]\n' >&2; exit 2; }
repo=$(cd -- "$(dirname -- "$0")/.." && pwd -P)
go_bin=$(command -v go)
git_bin=$(command -v git)
bash_bin=$(command -v bash)
python_bin=$(command -v python3)
go_cache=$("$go_bin" env GOCACHE)
go_modcache=$("$go_bin" env GOMODCACHE)
root=$(mktemp -d "${TMPDIR:-/tmp}/autoaccount-demo.XXXXXXXX")
trap 'rm -rf -- "$root"' EXIT
mkdir -p "$root/tools" "$root/home" "$root/config" "$root/data" "$root/state" "$root/cache" "$root/gh-config"
ln -s "$git_bin" "$root/tools/git"
ln -s "$bash_bin" "$root/tools/bash"
ln -s "$python_bin" "$root/tools/python3"
if [[ $# == 0 ]]; then
  for tool in vhs ttyd ffmpeg; do
    path=$(command -v "$tool") || { printf 'Missing dependency: %s\n' "$tool" >&2; exit 1; }
    ln -s "$path" "$root/tools/$tool"
  done
  browser=$(command -v chromium || command -v chromium-browser || command -v google-chrome) || { printf 'Missing Chromium browser\n' >&2; exit 1; }
  ln -s "$browser" "$root/tools/chromium"
fi
cd "$repo"
env -i PATH="$(dirname "$go_bin"):/usr/bin:/bin" HOME="$root/home" GOCACHE="$go_cache" GOMODCACHE="$go_modcache" GOPROXY=off GOSUMDB=off GOENV=off GOWORK=off GOTOOLCHAIN=local "$go_bin" build -o "$root/gh-autoaccount" ./cmd/gh-autoaccount
common=(PATH="$root/tools:/usr/bin:/bin" HOME="$root/home" XDG_CONFIG_HOME="$root/config" XDG_DATA_HOME="$root/data" XDG_STATE_HOME="$root/state" XDG_CACHE_HOME="$root/cache" GH_CONFIG_DIR="$root/gh-config" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL="$root/gitconfig" GIT_TERMINAL_PROMPT=0 GH_PROMPT_DISABLED=1 GIT_ASKPASS=/bin/false SSH_ASKPASS=/bin/false DBUS_SESSION_BUS_ADDRESS=unix:path="$root/no-dbus" TMPDIR="$root" HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 ALL_PROXY=http://127.0.0.1:1 NO_PROXY= PS1='demo:~$ ' DEMO_REPO="$repo" DEMO_ROOT="$root" DEMO_BINARY="$root/gh-autoaccount")
if [[ $# == 1 ]]; then
  env -i "${common[@]}" "$bash_bin" --noprofile --norc "$repo/demo/session.sh" --check
else
  env -i "${common[@]}" "$root/tools/vhs" demo/demo.tape
fi
