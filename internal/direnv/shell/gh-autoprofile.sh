#!/usr/bin/env bash
# gh-autoprofile direnv shell library
# Installed under direnv's active config root by `gh autoaccount setup`
# (for example $DIRENV_CONFIG/lib/gh-autoprofile.sh or ~/.config/direnv/lib/gh-autoprofile.sh)
#
# Provides the use_gh_autoprofile function for .envrc files.
#
# Wrapper mode (deprecated; native routing via `gh autoaccount setup` is the default):
#   use_gh_autoprofile <user> [git-email] [git-name] [ssh-key-path]
#   Exports only GH_AUTOPROFILE_USER and GH_AUTOPROFILE_MODE=wrapper.
#   The shell hook creates gh()/git() functions that inject the token
#   per-invocation.
#
# Export mode (deprecated; opt-in per pin with --export-token):
#   use_gh_autoprofile_export <user> [git-email] [git-name] [ssh-key-path]
#   Exports GH_TOKEN and GITHUB_TOKEN directly into the shell environment.

# --- wrapper mode (deprecated) ------------------------------------------------

use_gh_autoprofile() {
  local user="$1"
  local git_email="${2:-}"
  local git_name="${3:-}"
  local ssh_key="${4:-}"

  if [[ -z "$user" ]]; then
    _gh_autoprofile_clear_profile
    log_error "gh-autoaccount: usage: use_gh_autoprofile <gh-username> [git-email] [git-name] [ssh-key-path]"
    return 1
  fi

  # Wrapper mode must own GitHub auth for this directory. Clear any ambient
  # GitHub tokens so the prompt hook installs wrappers and plain `gh`/`git`
  # commands cannot bypass the pinned user.
  unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST GH_AUTOPROFILE_HOST

  local record actual_user host mode rest
  if ! record=$(_gh_autoprofile_exec_pin "$user" /bin/sh -c '
    test "$GH_AUTOPROFILE_MODE" = wrapper || exit 1
    printf "%s\n%s\n%s" "$GH_AUTOPROFILE_USER" "$GH_AUTOPROFILE_HOST" "$GH_AUTOPROFILE_MODE"
  '); then
    _gh_autoprofile_clear_profile
    log_error "gh-autoaccount: could not resolve directory pin for '$user'"
    return 1
  fi
  if [[ ${#record} -gt 512 || "$record" != *$'\n'* ]]; then _gh_autoprofile_clear_profile; return 1; fi
  actual_user=${record%%$'\n'*}; rest=${record#*$'\n'}
  if [[ "$rest" != *$'\n'* ]]; then _gh_autoprofile_clear_profile; return 1; fi
  host=${rest%%$'\n'*}; mode=${rest#*$'\n'}
  if [[ -z "$actual_user" || -z "$host" || "$mode" != wrapper ]]; then _gh_autoprofile_clear_profile; return 1; fi

  # Export only non-sensitive markers. The shell hook reads these to create
  # per-invocation wrapper functions.
  export GH_AUTOPROFILE_USER="$actual_user" GH_AUTOPROFILE_HOST="$host" GH_AUTOPROFILE_MODE=wrapper

  if ! _gh_autoprofile_identity "$git_email" "$git_name" "$ssh_key"; then
    _gh_autoprofile_clear_profile
    return 1
  fi

  log_status "gh-autoaccount" "deprecated: wrapper mode does not cover non-interactive shells; run 'gh autoaccount setup --native'"
  log_status "gh-autoaccount" "activated '$user' (wrapper mode)"
}

# --- export mode (opt-in) -----------------------------------------------------

use_gh_autoprofile_export() {
  local user="$1"
  local git_email="${2:-}"
  local git_name="${3:-}"
  local ssh_key="${4:-}"

  if [[ -z "$user" ]]; then
    _gh_autoprofile_clear_profile
    log_error "gh-autoaccount: usage: use_gh_autoprofile_export <gh-username> [git-email] [git-name] [ssh-key-path]"
    return 1
  fi

  local record actual_user host mode token rest
  if ! record=$(_gh_autoprofile_exec_pin "$user" /bin/sh -c '
    case "$GH_AUTOPROFILE_HOST" in github.com|*.ghe.com) token=$GH_TOKEN ;; *) token=$GH_ENTERPRISE_TOKEN ;; esac
    test "$GH_AUTOPROFILE_MODE" = export && test -n "$token" && test "${#token}" -le 4096 || exit 1
    printf "%s\n%s\n%s\n%s" "$GH_AUTOPROFILE_USER" "$GH_AUTOPROFILE_HOST" "$GH_AUTOPROFILE_MODE" "$token"
  '); then
    _gh_autoprofile_clear_profile
    log_error "gh-autoaccount: could not resolve export pin for '$user'"
    return 1
  fi
  unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST
  if [[ ${#record} -gt 4608 || "$record" != *$'\n'* ]]; then _gh_autoprofile_clear_profile; return 1; fi
  actual_user=${record%%$'\n'*}; rest=${record#*$'\n'}
  if [[ "$rest" != *$'\n'* ]]; then _gh_autoprofile_clear_profile; return 1; fi
  host=${rest%%$'\n'*}; rest=${rest#*$'\n'}
  if [[ "$rest" != *$'\n'* ]]; then _gh_autoprofile_clear_profile; return 1; fi
  mode=${rest%%$'\n'*}; token=${rest#*$'\n'}
  if [[ -z "$actual_user" || -z "$host" || "$mode" != export || -z "$token" || "$token" == *[[:space:][:cntrl:]]* ]]; then _gh_autoprofile_clear_profile; return 1; fi
  case "$host" in
    github.com|*.ghe.com) export GH_TOKEN="$token" GITHUB_TOKEN="$token" ;;
    *) export GH_ENTERPRISE_TOKEN="$token" GITHUB_ENTERPRISE_TOKEN="$token" ;;
  esac
  if [[ "$host" != github.com ]]; then export GH_HOST="$host"; fi
  export GH_AUTOPROFILE_USER="$actual_user" GH_AUTOPROFILE_HOST="$host" GH_AUTOPROFILE_MODE=export

  if ! _gh_autoprofile_identity "$git_email" "$git_name" "$ssh_key"; then
    _gh_autoprofile_clear_profile
    return 1
  fi

  log_status "gh-autoaccount" "deprecated: export mode leaves a token in the environment; use 'gh autoaccount exec -- <command>'"
  log_status "gh-autoaccount" "activated '$user' (export mode)"
}

# --- shared helpers -----------------------------------------------------------

_gh_autoprofile_clear_profile() {
  unset GH_TOKEN GITHUB_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN GH_HOST GH_AUTOPROFILE_USER GH_AUTOPROFILE_HOST GH_AUTOPROFILE_MODE
}

_gh_autoprofile_exec_pin() {
  local expected="$1"
  shift
  GH_PATH=@GH_AUTOPROFILE_REAL_GH@ GH_AUTOPROFILE_REAL_GH=@GH_AUTOPROFILE_REAL_GH@ GH_AUTOPROFILE_DISABLE_SHIM=1 \
    @GH_AUTOPROFILE_EXTENSION@ exec --dir "$PWD" --require-pin --expect-user "$expected" -- "$@"
}

_gh_autoprofile_identity() {
  local git_email="$1"
  local git_name="$2"
  local ssh_key="$3"

  # Git identity
  if [[ -n "$git_email" ]]; then
    export GIT_AUTHOR_EMAIL="$git_email"
    export GIT_COMMITTER_EMAIL="$git_email"
  fi

  if [[ -n "$git_name" ]]; then
    export GIT_AUTHOR_NAME="$git_name"
    export GIT_COMMITTER_NAME="$git_name"
  fi

  # SSH key selection. Git evaluates GIT_SSH_COMMAND through a shell, so quote
  # the path for that second parsing pass rather than interpolating it raw.
  if [[ -n "$ssh_key" ]]; then
    if [[ ! -f "$ssh_key" ]]; then
      log_error "gh-autoaccount: SSH key not found: $ssh_key"
      return 1
    fi
    if [[ "$ssh_key" == *$'\n'* || "$ssh_key" == *$'\r'* || "$ssh_key" == *$'\t'* ]]; then
      log_error "gh-autoaccount: SSH key path contains a control character"
      return 1
    fi
    local quoted_ssh_key
    quoted_ssh_key=$(_gh_autoprofile_quote_command_arg "$ssh_key")
    export GIT_SSH_COMMAND="ssh -i ${quoted_ssh_key} -o IdentitiesOnly=yes"
  fi
}

_gh_autoprofile_quote_command_arg() {
  printf '%q' "$1"
}
