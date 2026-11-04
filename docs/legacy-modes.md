# Legacy wrapper and export modes (deprecated)

Native routing (see the [README](../README.md)) is the default for a fresh install
on Linux and macOS. The modes below predate it. They still work, and an existing
legacy installation keeps legacy routing until you run `gh autoaccount setup
--native`, but new setups should not use them. `doctor` and `status` report when
legacy routing is active.

Both modes use [direnv](https://direnv.net/): `gh autoaccount pin` writes a managed
block into the pinned directory's `.envrc`, delimited by `# gh-autoprofile:start`
and `# gh-autoprofile:end`. Preserve any custom content in that file. Values
written to `.envrc` use POSIX single-quote escaping. Native routing is not
available on Windows, so these modes are the only integration the code retains
there; complete Windows shell integration is not established.

## Wrapper mode

1. direnv exports only non-sensitive profile markers (`GH_AUTOPROFILE_USER`,
   `GH_AUTOPROFILE_MODE`) and clears conflicting GitHub token and host variables
   for the directory session.
2. A shell hook defines `gh()` and `git()` wrapper functions on each prompt.
3. Each wrapper re-resolves the trusted directory pin, injects the matching
   host-class credential only into the child process and runs the real command.
4. The injected token does not sit in the parent shell environment.

The wrappers need a prompt cycle, so they miss non-interactive shells: agents,
editor tasks, cron and one-shot subprocesses. Without native routing, the
credential helper or `exec`, ordinary global `gh` and Git behavior applies there,
not the pin. Options that cover those contexts without switching to native
routing:

```bash
gh autoaccount exec --require-pin --expect-user bob --verify-identity -- gh pr status
gh autoaccount setup --gh-shim            # PATH-level shim for plain gh
gh autoaccount setup --credential-helper  # GitHub HTTPS git without prompt hooks
```

Return to legacy routing from native with `gh autoaccount setup --native=false`,
then reload the hook or restart the shell. The Git helper and identity settings
stay configured, and `setup --migrate` and `doctor --fix` preserve the selected
mode. The switch itself can complete without direnv, but wrappers and export need
direnv to activate.

## Export mode

Export mode (`pin --export-token`) makes direnv put the selected host class's token
pair into the shell environment so third-party tools can read it. Prefer
`gh autoaccount exec -- <command>` for a command-scoped token. **Tokens in the
environment are visible to the shell and every child process it starts.** Use
this only where a tool requires it. Owner pins do not support `--export-token`.

Direnv scopes and unloads these variables when you leave the directory.

### Variables set by legacy modes

| Variable | Mode | Purpose |
|---|---|---|
| `GH_AUTOPROFILE_USER` | wrapper, export | Non-sensitive marker read by the shell hook |
| `GH_AUTOPROFILE_MODE` | wrapper, export | Selected mode (`wrapper` or `export`) |
| `GIT_AUTHOR_EMAIL`, `GIT_COMMITTER_EMAIL` | both, if a pin sets an email | Git commit identity |
| `GIT_AUTHOR_NAME`, `GIT_COMMITTER_NAME` | both, if a pin sets a name | Git commit identity |
| `GIT_SSH_COMMAND` | both, if a pin sets an SSH key | Per-directory SSH key |
| `GH_TOKEN`, `GITHUB_TOKEN` | export | Credential for `github.com` or `*.ghe.com` |
| `GH_ENTERPRISE_TOKEN`, `GITHUB_ENTERPRISE_TOKEN` | export | Credential for another Enterprise Server host |
| `GH_HOST` | export | Selected non-default host |

With `setup --git-identity`, managed `.envrc` blocks use Git config instead of the
identity variables. Explicit identity environment variables remain Git overrides.
Without it, identity is environment-based, so `git config user.email` does not
show it.

### Ambient tokens in export mode

Inherited token variables are ignored by default and the pin's named account is
resolved. For a headless process that cannot reach the keyring, the caller may
explicitly trust an ambient token:

```bash
export GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN=1
export GH_TOKEN=... # caller asserts this token belongs to the pinned account
```

The opt-in is accepted only without `token_command`, and when either both ownership
markers (`GH_AUTOPROFILE_USER`, `GH_AUTOPROFILE_HOST`) are absent (first use) or
both match the selected `(host, user)`. A partial or mismatched pair resolves the
configured credential instead. A `token_command` stays authoritative.
The opt-in asserts ownership; it does not verify remote identity. Headless `gh auth
token` keyring access may need a D-Bus session, which is the usual reason to use it.

## Externally managed shells

```bash
gh autoaccount setup --no-shell-rc
gh autoaccount setup --no-shell-rc --gh-shim --credential-helper --git-identity
```

`--no-shell-rc` persists external-shell mode in `settings.yml` in the config
directory. Setup installs the library and hook files but does not create, edit or
remove RC files or PATH blocks, including existing managed ones. Later setup,
`--migrate` and `doctor --fix` respect the preference; `setup --no-shell-rc=false`
restores automatic RC integration. Through your shell manager, configure the
direnv hook, source the installed `hook.sh`, and put the shim directory
(`~/.local/bin`) before the real `gh` on PATH. Installing a shim does not activate
it. `status` and `doctor` report external integration as not verified, because a
subprocess cannot inspect the parent shell.

## Migrating from legacy wrapper blocks

Managed or malformed old wrapper `.envrc` content blocks native `setup`, `pin`,
`unpin` and `--migrate` before any state change, and inspection errors fail
closed. To clear the block:

1. Review the entire `.envrc`.
2. Run `direnv deny` for it.
3. Remove only the managed block, preserving all custom bytes.
4. Reload the shell and clear old exports.
5. Retry.

There is no automatic cleanup or recovery backup, and these checks neither sandbox
nor prevent concurrent same-user races. After upgrading, run
`gh autoaccount setup --migrate` (it refreshes the installed library, hook and
helper/shim assets, backfills missing pin modes, regenerates enabled identity
includes and respects the external-shell preference), then restart the shell.
Legacy and export managed blocks may be updated; migration runs `direnv allow`
automatically only when the `.envrc` has no content outside the managed markers and
the safety checks pass. Otherwise review the full file and allow it manually.
Review `setup --migrate` and `doctor --fix` before running them, since both change
configuration.
