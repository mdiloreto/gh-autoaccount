#!/usr/bin/env bash
# gh-autoprofile shell hook — process-scoped token injection
# Installed to ~/.config/gh-autoprofile/hook.sh by `gh autoaccount setup`
# Sourced from ~/.zshrc or ~/.bashrc.
#
# When GH_AUTOPROFILE_USER is set (by direnv), this hook reads
# GH_AUTOPROFILE_MODE to decide whether to create wrapper functions or rely
# on exported tokens.
#
# When GH_AUTOPROFILE_USER is unset (leaving the directory), the wrapper
# functions are removed and the original commands are restored.

# Guard: only load this hook revision once per shell session. Bump the revision
# whenever installed hook behavior changes so sourcing the shell RC upgrades an
# older in-memory hook without duplicating prompt registration.
_gh_autoprofile_installed_native=0
if [[ "${_GH_AUTOPROFILE_HOOK_LOADED:-}" == "6:$_gh_autoprofile_installed_native" ]]; then
  return 0
fi
_GH_AUTOPROFILE_HOOK_LOADED="6:$_gh_autoprofile_installed_native"

if [[ "$_gh_autoprofile_installed_native" == 1 ]]; then
  unset -f gh 2>/dev/null || true
  unset -f git 2>/dev/null || true
  _gh_autoprofile_hook() { :; }
  return 0
fi

# Track the last user we created wrappers for, so we don't recreate them
# on every prompt.
_gh_autoprofile_last_user=""
_gh_autoprofile_last_mode=""

_gh_autoprofile_find_executable() {
  if [[ -n "${ZSH_VERSION:-}" ]]; then
    whence -p -- "$1" 2>/dev/null
  else
    type -P -- "$1" 2>/dev/null
  fi
}

# Read only the magic bytes, not an entire native executable into a variable.
_gh_autoprofile_is_script() {
  local _magic=""
  local LC_ALL=C
  if [[ -n "${ZSH_VERSION:-}" ]]; then
    IFS= read -r -k 2 -u 0 _magic < "$1" || return 1
  else
    IFS= read -r -n 2 _magic < "$1" || return 1
  fi
  [[ "$_magic" == '#!' ]]
}

_gh_autoprofile_is_native() {
  local _magic=""
  local LC_ALL=C
  if [[ -n "${ZSH_VERSION:-}" ]]; then
    IFS= read -r -k 4 -u 0 _magic < "$1" || return 1
  else
    IFS= read -r -n 4 _magic < "$1" || return 1
  fi
  case "$_magic" in
    $'\x7fELF'|$'\xfe\xed\xfa\xce'|$'\xce\xfa\xed\xfe'|$'\xfe\xed\xfa\xcf'|$'\xcf\xfa\xed\xfe'|$'\xca\xfe\xba\xbe'|$'\xbe\xba\xfe\xca'|$'\xca\xfe\xba\xbf'|$'\xbf\xba\xfe\xca') return 0 ;;
    *) return 1 ;;
  esac
}

# Decode only our generated shim's literal quoting; never evaluate shell text.
_gh_autoprofile_unwrap_gh_shim() {
  local _first _signature _options _bypass _forward _quoted _target _roundtrip _extension _extension_quoted _prefix
  local _escaped_quote="'\\''"
  if ! _gh_autoprofile_is_script "$1"; then
    printf '%s' "$1"
    return 0
  fi
  {
    IFS= read -r _first
    IFS= read -r _signature || true
    IFS= read -r _options || true
    IFS= read -r _bypass || true
    IFS= read -r _forward || true
  } < "$1"
  if [[ "$_signature" != '# gh-autoprofile-gh-shim' ]]; then
    printf '%s' "$1"
    return 0
  fi
  [[ "$_first" == '#!/usr/bin/env bash' && "$_options" == 'set -euo pipefail' ]] || return 1
  _quoted=${_bypass#'if [[ -n "${GH_AUTOPROFILE_DISABLE_SHIM:-}" ]]; then exec '}
  _quoted=${_quoted%' "$@"; fi'}
  [[ "$_quoted" == \'*\' ]] || return 1
  _target=${_quoted#\'}
  _target=${_target%\'}
  _target=${_target//\'\\\'\'/\'}
  _roundtrip="'${_target//\'/$_escaped_quote}'"
  [[ "$_quoted" == "$_roundtrip" &&
     "$_bypass" == 'if [[ -n "${GH_AUTOPROFILE_DISABLE_SHIM:-}" ]]; then exec '"$_quoted"' "$@"; fi' &&
     "$_target" == /* && -f "$_target" && -x "$_target" ]] || return 1
  _prefix="GH_AUTOPROFILE_REAL_GH=$_quoted exec "
  if [[ "$_forward" == "GH_PATH=$_quoted $_prefix"* ]]; then
    _prefix="GH_PATH=$_quoted $_prefix"
  fi
  [[ "$_forward" == "$_prefix"*' __shim-gh "$@"' ]] || return 1
  _extension_quoted=${_forward#"$_prefix"}
  _extension_quoted=${_extension_quoted%' __shim-gh "$@"'}
  [[ "$_extension_quoted" == \'*\' ]] || return 1
  _extension=${_extension_quoted#\'}
  _extension=${_extension%\'}
  _extension=${_extension//\'\\\'\'/\'}
  [[ "$_extension" == /* && "$_extension_quoted" == "'${_extension//\'/$_escaped_quote}'" ]] || return 1
  printf '%s' "$_target"
}

# Support a native interpreter or simple env [ -S | --split-string= ] name.
# Options, assignments, quoting, and script interpreters require manual review.
_gh_autoprofile_pin_interpreter() {
  local _line="" _interpreter="" _argument="" _extra=""
  [[ "$1" == /* && -f "$1" && -r "$1" && -x "$1" ]] || return 1
  if ! _gh_autoprofile_is_script "$1"; then
    _gh_autoprofile_is_native "$1"
    return $?
  fi
  IFS= read -r _line < "$1" || return 1
  IFS=$' \t' read -r _interpreter _argument _extra <<< "${_line#\#!}"
  [[ "$_interpreter" == /* ]] || return 1
  if [[ "${_interpreter##*/}" == env ]]; then
    if [[ "$_argument" == -S ]]; then
      _argument="$_extra"
      _extra=""
    elif [[ "$_argument" == --split-string=* ]]; then
      _argument=${_argument#--split-string=}
    fi
    [[ -n "$_argument" && -z "$_extra" && "$_argument" != -* &&
       "$_argument" != *[!a-zA-Z0-9_+.-]* ]] || return 1
    _interpreter=$(_gh_autoprofile_find_executable "$_argument") || return 1
  else
    [[ -z "$_argument" && -z "$_extra" ]] || return 1
  fi
  [[ "$_interpreter" == /* && -f "$_interpreter" && -r "$_interpreter" && -x "$_interpreter" ]] || return 1
  _gh_autoprofile_is_native "$_interpreter" || return 1
  printf '%s' "$_interpreter"
}

# Pin command paths when the hook is sourced, before a project .envrc can
# prepend an attacker-controlled directory to PATH. Keep them for the entire
# shell session rather than resolving them again on profile changes.
_gh_autoprofile_real_gh=$(_gh_autoprofile_find_executable gh || true)
_gh_autoprofile_real_git=$(_gh_autoprofile_find_executable git || true)
_gh_autoprofile_gh_ready=0
_gh_autoprofile_git_ready=0
_gh_autoprofile_gh_interpreter=""
_gh_autoprofile_git_interpreter=""
if _gh_autoprofile_real_gh=$(_gh_autoprofile_unwrap_gh_shim "$_gh_autoprofile_real_gh") &&
   _gh_autoprofile_gh_interpreter=$(_gh_autoprofile_pin_interpreter "$_gh_autoprofile_real_gh"); then
  _gh_autoprofile_gh_ready=1
fi
if _gh_autoprofile_git_interpreter=$(_gh_autoprofile_pin_interpreter "$_gh_autoprofile_real_git"); then
  _gh_autoprofile_git_ready=1
fi

_gh_autoprofile_run_gh() {
  local _run_token="$1"
  shift
  if [[ "$_gh_autoprofile_gh_ready" != 1 ]]; then
    echo 'gh-autoaccount: refusing gh: executable or interpreter could not be pinned safely; review it and start a trusted shell' >&2
    return 1
  fi
  local -a _command
  _command=("$_gh_autoprofile_real_gh")
  if [[ -n "$_gh_autoprofile_gh_interpreter" ]]; then
    _command=("$_gh_autoprofile_gh_interpreter" "${_command[@]}")
  fi
  if [[ -n "$_run_token" ]]; then
    GH_AUTOPROFILE_DISABLE_SHIM=1 GH_TOKEN="$_run_token" GITHUB_TOKEN="$_run_token" "${_command[@]}" "$@"
  else
    GH_AUTOPROFILE_DISABLE_SHIM=1 "${_command[@]}" "$@"
  fi
}

_gh_autoprofile_exec() {
  GH_PATH="$_gh_autoprofile_real_gh" GH_AUTOPROFILE_REAL_GH="$_gh_autoprofile_real_gh" \
    _gh_autoprofile_run_gh "" autoaccount exec --dir "$PWD" --require-pin \
      --expect-user "$GH_AUTOPROFILE_USER" -- "$@"
}

_gh_autoprofile_should_bypass_gh_auth() {
  if [[ "${1:-}" != "auth" ]]; then
    return 1
  fi
  case "${2:-}" in
    switch|login|logout|refresh|setup-git)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

_gh_autoprofile_hook() {
  local current_user="${GH_AUTOPROFILE_USER:-}"
  local current_mode="${GH_AUTOPROFILE_MODE:-}"

  # Backward compatibility for older installed shell libraries that only
  # export GH_AUTOPROFILE_USER.
  if [[ -z "$current_mode" && -n "$current_user" ]]; then
    current_mode="wrapper"
  fi

  # Nothing changed — skip.
  if [[ "$current_user" == "$_gh_autoprofile_last_user" && "$current_mode" == "$_gh_autoprofile_last_mode" ]]; then
    return 0
  fi

  if [[ -n "$current_user" && "$current_mode" == "wrapper" ]]; then
    # Wrapper mode: create wrapper functions that inject the pinned user's
    # token per-invocation.
    #
    gh() {
      if [[ "$_gh_autoprofile_gh_ready" != 1 ]]; then
        _gh_autoprofile_run_gh "" "$@"
        return $?
      fi
      if _gh_autoprofile_should_bypass_gh_auth "$@"; then
        _gh_autoprofile_run_gh "" "$@"
        return $?
      fi
      local -a _command
      _command=("$_gh_autoprofile_real_gh")
      if [[ -n "$_gh_autoprofile_gh_interpreter" ]]; then
        _command=("$_gh_autoprofile_gh_interpreter" "${_command[@]}")
      fi
      _gh_autoprofile_exec "${_command[@]}" "$@"
    }

    git() {
      # Strict wrapper mode requires the pinned token for every Git invocation,
      # including local and SSH commands; resolution failure never runs Git.
      if [[ "$_gh_autoprofile_git_ready" != 1 ]]; then
        echo 'gh-autoaccount: refusing git: executable or interpreter could not be pinned safely; review it and start a trusted shell' >&2
        return 1
      fi
      local -a _command
      _command=("$_gh_autoprofile_real_git")
      if [[ -n "$_gh_autoprofile_git_interpreter" ]]; then
        _command=("$_gh_autoprofile_git_interpreter" "${_command[@]}")
      fi
      _gh_autoprofile_exec "${_command[@]}" "$@"
    }

    _gh_autoprofile_last_user="$current_user"
    _gh_autoprofile_last_mode="wrapper"

  elif [[ -n "$current_user" && "$current_mode" == "export" ]]; then
    # Export mode: GH_AUTOPROFILE_USER is set and tokens are already in the
    # environment.
    # Tokens are already in the environment — no wrapper functions needed.
    # Remove any stale wrappers from a previous wrapper-mode directory.
    unset -f gh 2>/dev/null
    unset -f git 2>/dev/null
    _gh_autoprofile_last_user="$current_user"
    _gh_autoprofile_last_mode="export"

  else
    # User left a pinned directory — remove wrapper functions.
    unset -f gh 2>/dev/null
    unset -f git 2>/dev/null
    _gh_autoprofile_last_user=""
    _gh_autoprofile_last_mode=""
  fi
}

# Install the hook into the shell's prompt cycle.
if [[ -n "${ZSH_VERSION:-}" ]]; then
  # zsh: use precmd hook array (works alongside oh-my-zsh).
  autoload -Uz add-zsh-hook
  add-zsh-hook precmd _gh_autoprofile_hook
elif [[ -n "${BASH_VERSION:-}" ]]; then
  # bash: append to PROMPT_COMMAND.
  if [[ -z "${PROMPT_COMMAND:-}" ]]; then
    PROMPT_COMMAND="_gh_autoprofile_hook"
  elif [[ "$PROMPT_COMMAND" != *"_gh_autoprofile_hook"* ]]; then
    PROMPT_COMMAND="_gh_autoprofile_hook;${PROMPT_COMMAND}"
  fi
fi
