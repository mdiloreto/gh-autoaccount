#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd -P)
[[ $(uname -s) == Linux ]] || { printf '%s\n' 'This acceptance suite requires Linux.' >&2; exit 1; }
case $(uname -m) in
  x86_64|amd64|aarch64|arm64) ;;
  *) printf '%s\n' 'This acceptance suite requires Linux amd64 or arm64.' >&2; exit 1 ;;
esac
for tool in go git zsh bash jq; do
  if ! command -v "$tool" >/dev/null; then
    printf 'Missing required tool: %s (install it before running this suite).\n' "$tool" >&2
    exit 1
  fi
done
go_bin=$(readlink -f "$(command -v go)")
git_bin=$(readlink -f "$(command -v git)")
if [[ -n ${GH_AUTOPROFILE_TEST_GH:-} ]]; then
  gh_bin=$GH_AUTOPROFILE_TEST_GH
elif [[ -x /usr/bin/gh ]]; then
  gh_bin=/usr/bin/gh
else
  gh_bin=$(command -v gh || true)
fi
[[ $gh_bin == /* && -f $gh_bin && -x $gh_bin ]] || { printf '%s\n' 'Set GH_AUTOPROFILE_TEST_GH to an absolute path to the real executable gh CLI.' >&2; exit 1; }
gh_bin=$(readlink -f "$gh_bin")

# Preserve the pre-fetched module cache, while isolating user configuration and build cache.
modcache=$(GOENV=off GOWORK=off GOTOOLCHAIN=local "$go_bin" env GOMODCACHE)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/gh-autoprofile-offline.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
mkdir -p "$scratch/bin" "$scratch/home" "$scratch/cache" "$scratch/config" "$scratch/data" "$scratch/state"
ln -s "$go_bin" "$scratch/bin/go"
ln -s "$git_bin" "$scratch/bin/git"
ln -s "$gh_bin" "$scratch/bin/gh"

cd "$root"
env -i PATH="$scratch/bin:$PATH" HOME="$scratch/home" \
  XDG_CONFIG_HOME="$scratch/config" XDG_DATA_HOME="$scratch/data" \
  XDG_STATE_HOME="$scratch/state" XDG_CACHE_HOME="$scratch/cache" \
  GH_CONFIG_DIR="$scratch/gh" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
  GIT_TERMINAL_PROMPT=0 GH_PROMPT_DISABLED=1 GIT_ASKPASS=/bin/false SSH_ASKPASS=/bin/false \
  GH_NO_UPDATE_NOTIFIER=1 GH_NO_EXTENSION_UPDATE_NOTIFIER=1 \
  DBUS_SESSION_BUS_ADDRESS="unix:path=$scratch/no-dbus" \
  GOENV=off GOWORK=off GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local \
  GOMODCACHE="$modcache" GOCACHE="$scratch/go-cache" TMPDIR="$scratch" \
  HTTP_PROXY=http://127.0.0.1:1 HTTPS_PROXY=http://127.0.0.1:1 ALL_PROXY=http://127.0.0.1:1 NO_PROXY= \
  LC_ALL=C TZ=UTC GH_AUTOPROFILE_TEST_GH="$gh_bin" \
  bash --noprofile --norc -e -u -o pipefail -c '
    if ! version=$("$GH_AUTOPROFILE_TEST_GH" --version) || [[ $version != "gh version "* ]]; then
      printf "%s\n" "Invalid gh CLI version output; set GH_AUTOPROFILE_TEST_GH to the real gh executable." >&2
      exit 1
    fi
    printf "Using Go: %s\nUsing Git: %s\nUsing gh: %s\n%s\n" "$(command -v go)" "$(command -v git)" "$GH_AUTOPROFILE_TEST_GH" "$version"
    go build -o "$TMPDIR/gh-autoprofile" ./cmd/gh-autoaccount
    go test -count=1 ./scripts
    bash scripts/smoke-agent-workflow.sh "$TMPDIR/gh-autoprofile"
    bash scripts/smoke-native-routing.sh "$TMPDIR/gh-autoprofile"
    bash scripts/smoke-onboarding.sh "$TMPDIR/gh-autoprofile"
  '
