# Agent and tool shells: execution contract

This describes the intended beta interface, not a claim that a release or its
runtime checks have passed. Prompt hooks may not run in agent shells, editor tasks,
cron or one-shot subprocesses. Check the actual executable's help before use.

## Execution contract

`gh autoaccount exec -- <command>` resolves a directory context, passes credentials
to the child, forwards streams and exit code, and does not mutate the parent or
switch the globally active account. `--dir` selects pin lookup and child working
directory; pins match canonical physical paths and inherit from parents.

Without `--require-pin`, selection precedence is explicit `exec --user`, then
deepest real matching pin, then configured `github.com` default, then unselected
legacy behavior. Explicit selection without `--require-pin` inherits no pin host,
broker, identity or SSH metadata. With `--require-pin`, a matching actual pin is
retained with its host, token command, identity and SSH metadata; a conflicting
`--user` is rejected and a default is not a pin. Default selection also does not
inherit pin metadata. External Git configuration and explicit
environment overrides still apply. Pin identity/SSH metadata applies when the pin
is selected; with Git-native identity enabled, Git config supplies identity instead
of injected pin variables. Unguarded `exec` does not reject unpinned directories.

### Guards

| Guard | Meaning |
|---|---|
| `--require-pin` | Require an actual matching directory pin, not a default, and reject conflicting explicit selection. |
| `--expect-user NAME` | Compare the selected profile name case-insensitively; empty, missing or mismatched selection fails before running the child. |
| `--verify-identity` | Check the remote principal before running the child. |

Use all three without `--user` to assert the directory pin and verify the remote
login. The first two alone do not prove remote identity. For example, in a
disposable workspace with a reviewed pin:

```bash
gh autoaccount status --dir ~/work-project --json
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- gh api user --jq .login
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- git var GIT_AUTHOR_IDENT
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- git var GIT_COMMITTER_IDENT
```

Compare the remote login and both Git identities independently. `gh --repo` does
not select an authentication account. JSON status is local diagnostics, not remote
identity verification. Check exit status and stop before writes on mismatch.

`--expect-user` does not alter credential-source precedence. Wrapper pins use
named credentials. Export pins do too unless the caller explicitly sets
`GH_AUTOPROFILE_ALLOW_AMBIENT_TOKEN=1` and ownership markers are both absent or
both present and match the pin's `(host, user)`. Partial or mismatched markers
resolve the configured named/broker credential instead. A `token_command` remains
authoritative. Ambient opt-in trusts the caller, not a verified token identity.
Matching login alone does not guarantee repository authorization; approve writes
separately, retaining the checked directory and guarded execution context.

## Integration and trust boundaries

Native routing uses the pin registry and needs no direnv or `.envrc` for fresh
wrapper pins. Git HTTPS authentication, SSH and Git identity remain distinct.
`setup --git-identity`, shell RC and PATH changes are separate opt-ins; external
shell mode leaves activation to the shell manager. Export and legacy mode still
require direnv. Windows binaries do not establish full Windows shell support.

Command-scoped injection does not sandbox child commands or same-user processes;
export mode exposes tokens to the shell and its children. A trusted token broker
is an authoritative executable, not a sandbox. Repository content and remote
metadata do not select credentials. Never include secrets in feedback.

The compatibility namespace (`GH_AUTOPROFILE_*`, storage, managed markers and
`use_gh_autoprofile`) is deliberately retained. For blocked managed or malformed
old wrapper `.envrc` files, follow the manual review, deny, block removal and shell
reload procedure in the [README](../README.md#compatibility-and-safe-migration).
Inspection errors fail closed; no automatic cleanup, backups or race guarantees
are provided. Setup and migration require separate approval.

### Optional integrations

| Integration | Purpose |
|---|---|
| `setup --gh-shim` | PATH-level convenience for plain `gh`; requires correct PATH placement. |
| `setup --credential-helper` | Exact-host GitHub HTTPS authentication without prompt hooks; does not choose Git identity or SSH keys, and fails closed on credential resolution failure. |
| `setup --git-identity` | Global Git conditional includes for pin identity fields, without repository-local config writes. |
| `setup --alias` | Opt in to `aa` mapping exactly to `autoaccount`; existing mappings are not overwritten. |

These are persistent opt-in changes, not automatic recovery from a 404. Migration
and `doctor --fix` also change configuration. In the tested GitHub CLI version,
`gh extension exec autoaccount` requires authentication; the literal
`gh autoaccount` command supports credential-free dispatch. Only the exact
configured `aa` to `autoaccount` mapping also receives native recovery bypass.

A pin may specify `--host` and `--token-command`. The latter is a trusted absolute
executable run per command without a shell, with sanitized credential variables,
a timeout and bounded single-token output; failure does not fall back to ambient
or keyring credentials. Native `gh` receives only the host class's primary token
variable. Explicit `exec` supplies its child the matching primary/fallback pair
after conflicting token and host variables are removed. GitHub token variables
do not isolate exact hosts within a host class. The remote verifier accepts the
GitHub Enterprise Managed User `LOGIN_SHORTCODE` form (one underscore,
alphanumeric shortcode, 39 characters total); the API must return the full login.

Initial real-`gh` resolution trusts externally supplied absolute `GH_PATH`, or
otherwise trusted startup `PATH`. Generated helpers, shims and shell libraries
embed absolute routes and replace inherited routing markers. A same-user process
can still replace their targets. See [SECURITY.md](../SECURITY.md) for the full
trust model, interpreter restrictions and auto-allow race limitations.

## Local JSON versus remote verification

```bash
gh autoaccount status --dir ~/work-project --json
gh autoaccount list --json
```

Both emit one JSON document with `schema_version: 1`. Check exit status before
decoding. Status may look up credentials locally and invokes Git diagnostics;
list only reads pins. Neither performs HTTP verification, scope inspection or
`gh auth status`, nor emits tokens or fragments.

| Status field | Meaning |
|---|---|
| `directory` | Absolute target directory. |
| `pin` | Object or `null`, containing `user`, normalized `host`, `token_command_configured`, `directory`, `mode`, and optional `git_name`, `git_email`, `ssh_key`; never the broker path. |
| `environment` | Inherited `profile_user`, `profile_host`, `profile_mode`, and `gh_token_present`, `github_token_present`, `gh_enterprise_token_present`, `github_enterprise_token_present`; not target-directory activation. |
| `authentication` | `selected`, `source`, `credential_available`, `verified` (always false), and optional `error`. Availability does not establish validity, login, permissions or SSO. |
| `git` | Optional `git_dir`, `author_ident`, `committer_ident`, `error`: actual Git results in the target directory under current config/environment, without pin identity injection. |

Authentication sources include `keyring`, `token_command`, the four supported
GitHub token variable names, `default`, and `unavailable`. Errors have `code` and
`message`: credential lookup may report `credential_unavailable`, and Git may
report `repository_unavailable` or `identity_unavailable` with partial results.
Inspect errors even on command success. Invalid registry/settings or an inaccessible
target directory can instead fail the command. List emits `pins` with the same pin
objects, or `[]`. Human-readable and JSON status are local-only; both report actual
Git config/environment, not hypothetical pin overrides in a future child.

## Safe acceptance sequence

With an existing reviewed pin in the intended workspace, run these checks from
the same non-interactive context in which later work will run:

```bash
gh autoaccount status --dir ~/work-project --json
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- gh api --hostname github.com user --jq .login
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- git var GIT_AUTHOR_IDENT
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- git var GIT_COMMITTER_IDENT
```

1. Inspect local pin, credential availability and errors, and Git diagnostics.
2. Require the API login and both Git identities to match the intended account,
   names and emails. Matching login alone does not guarantee repository access.
3. Stop on any mismatch or error. Approve writes separately, preserving the exact
   directory and guarded execution context used for verification.

Do not print credentials, dump the environment, or mutate global account state as
an identity check. A 404 may indicate the wrong repository/login, missing access or
missing SSO. If an explicit account is needed without `--require-pin`, remember it
does not carry pin host, broker, Git identity or SSH metadata.

### Linked worktrees

Use the checkout path for `--dir`, and inspect `git -C PATH rev-parse
--absolute-git-dir`. Git-native `includeIf gitdir` matches the actual Git
directory, often inside the original repository, not necessarily the checkout.
Account pin selection and identity includes may differ. Local Git config and
explicit identity variables can override includes. Repeat both `git var` checks
inside the intended exec context; status does not model transient pin injection.

## Offline smoke checks

Run `bash scripts/smoke-agent-workflow.sh /absolute/path/to/candidate` with Bash,
Git and Go. It creates synthetic pins, a fake gh and real linked worktrees in
temporary directories; checks inherited pins, concurrent no-TTY execution, guarded
EMU verification, ambient ownership markers, broker success/failure, pre-child
guard failures, JSON parsing/redaction, distinct Git identities and unchanged
parent context. It removes its exact scratch directory; `TMPDIR` selects an
existing scratch root. No real keyring, pins, setup or GitHub access is used.

This complements unit tests, not live keyring access, interactive activation,
Git-native include selection, remote permissions or release provenance. Rerun on
the integrated candidate. A headless keyring can fail; ambient recovery requires
an export pin without `token_command`, explicit opt-in and absent or matching
complete ownership markers. Do not print credentials, dump environment variables
or mutate the global account as an identity check. A 404 can mean wrong
repository/login, missing access or missing SSO.

See [offline testing](testing.md) for isolated acceptance checks and their limits.
