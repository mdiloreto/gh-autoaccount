# Routing and agent reference

Details behind the [README](../README.md): how an account is selected, owner-pin
rules, the guards on `exec`, the JSON output, and linked-worktree caveats.

## Selection precedence

Without `--require-pin`: explicit `exec --user`, then the session override
`GH_AUTOACCOUNT_USER`, then the owner pin for the accessed repository, then the
deepest directory pin, then the configured `github.com` default, then unselected
behavior (ordinary global `gh`/Git). A default is not a pin and does not inherit pin
metadata; neither does an explicit `--user` or the session override. The Git helper
applies the session override only to github.com requests.

With `--require-pin`, the pin is kept, and an explicit `--user` or a session override
that names a different login fails before any credential is resolved. `status`
reports a session selection as source `session`.

Pins match canonical physical directories, including symlink aliases; the deepest
match wins. Accounts are identified by `(host, user)`, with a missing host meaning
`github.com`.

## Pin options

```bash
gh autoaccount pin bob --dir ~/work --git-email bob@example.test --git-name "Bob Example"
gh autoaccount pin alice --dir ~/project --ssh-key ~/.ssh/id_example
gh autoaccount pin bob --dir ~/enterprise --host github.example.com \
  --token-command /absolute/path/to/trusted-broker
```

A `--token-command` is a trusted absolute executable run per command without a
shell, with sanitized credential variables, a timeout and bounded single-token
output. It is authoritative: failure never falls back to ambient or keyring
credentials. Trust the executable and its dependencies. Its path is never shown in
`list --json` or `status --json`.

The remote verifier accepts GitHub Enterprise Managed User logins in the
`LOGIN_SHORTCODE` form (one underscore, alphanumeric shortcode, at most 39
characters); the API must return the complete login. The registry rejects unknown
fields, so a binary older than a given pin field (`host`, `token_command`, `owner`)
cannot read a registry that contains it. Remove the field with the current binary or
restore a reviewed backup before rolling back.

## Owner pins

Git runs credential helpers in the invoking directory, so
`git clone https://github.com/work-org/x ~/work/x` from `~` authenticates as the
default account, not the `~/work` pin. Pin the owner instead:

```bash
gh autoaccount pin bob --owner work-org --git-email bob@example.test
gh autoaccount unpin --owner work-org            # add --host for Enterprise
```

An owner pin has an `owner` and no `dir`, matches the owner case-insensitively and
is unique per
`(host, owner)`. `exec --require-pin` accepts an owner or a directory pin.

The owner comes from, in order: the Git credential request path, gh's `-R/--repo`,
`GH_REPO`, the target of `gh repo clone` (which ignores both `-R` and `GH_REPO`),
then the working directory's remote: the one marked `gh-resolved`, else `upstream`,
`github`, `origin` (gh's order), else the only remote.

For `gh`, owner routing is a best-effort reading of the command line, not a sandbox.
Routing is skipped (the directory pin or default applies) when the target is unknown:

- `-R/--repo` appears right after another flag, so it could be that flag's value;
  a repeated `-R` is resolved by gh to its last value. Put `-R` first or right
  after a positional argument.
- `-R`/`GH_REPO` is unparseable, or the command is a bare `gh repo clone REPO`.
- An argument names a repository under another owner (a positional `OWNER/REPO` or
  URL such as `gh repo view other-org/x`, or an `api` path under
  `repos/OWNER/REPO`, including flag values that merely look like one).

`gh api` routes only by its own endpoint: a `repos/OWNER/REPO` path on its
`--hostname` host, or `{owner}` placeholders resolved like gh's base repository on
the same host. GraphQL and other endpoints get no owner routing. A command can still
reach repositories its arguments do not name. The Git HTTPS helper routes exactly by
Git's request, and no path sends a pin's credential to a host other than the pin's.

Git identity: with `setup --git-identity`, `git_email`/`git_name` on an owner pin
apply to repositories with a remote URL under that owner (https or ssh, with or
without userinfo or port, or scp-style) and win over directory identity. This needs
Git 2.36 or newer; setup and doctor refuse or flag older Git. Git can only match
`remote.*.url`, so identity matches every remote while credentials follow the one
selection above. A checkout whose remote falls under an owner pin that credentials
do not select (another remote wins, or several owner pins match) can disagree;
`status` warns about it (`conflicting_owner_pins` in `--json`).

For the Git path, setup and pin also set `credential.https://<host>.useHttpPath=true`
on hosts that have an owner pin, because Git otherwise omits the repository path
from helper requests; an explicit `false` is overwritten with a note. It is never
unset automatically, even after the last owner pin is removed. Every other helper in
the chain, including the `gh auth git-credential` fallback, then sees per-path
requests, and path-keyed credential stores keep one entry per repository. `doctor`
reports a missing setting only for hosts with owner pins and `doctor --fix`
restores it.

## Guards on `exec`

| Flag | Meaning |
|---|---|
| `--require-pin` | Require an actual owner or directory pin, not a default; reject a conflicting `--user`. The matching pin keeps its host, token command, identity and SSH metadata. |
| `--expect-user NAME` | Compare the selected profile name case-insensitively; a missing or mismatched selection fails before the child runs. This is not a remote check. |
| `--verify-identity` | Check the credential's remote login before running the child. |

Use all three, without `--user`, to assert the pin and verify the remote login.
Matching login alone does not prove repository authorization, and `gh --repo` does
not select an authentication account. `--dir` sets both the pin lookup and the
child's working directory. Without `--require-pin`, `exec` does not reject unpinned
directories.

Pre-write check for an agent, from the same non-interactive context later work will
use:

```bash
gh autoaccount status --dir ~/work-project --json
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- gh api --hostname github.com user --jq .login
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- git var GIT_AUTHOR_IDENT
gh autoaccount exec --dir ~/work-project --require-pin --expect-user bob --verify-identity -- git var GIT_COMMITTER_IDENT
```

Require the login and both Git identities to match, and stop before writing on any
mismatch or error. Do not print credentials or dump the environment. A 404 can mean
the wrong repository or login, missing access or missing SSO.

## Environment seen by children

Native `gh` receives only the selected host class's primary token: `GH_TOKEN` for
`github.com` and `*.ghe.com`, or `GH_ENTERPRISE_TOKEN` for other Enterprise hosts.
A non-default host also receives `GH_HOST`. `exec` supplies the matching
primary/fallback pair (`GH_TOKEN`/`GITHUB_TOKEN` or
`GH_ENTERPRISE_TOKEN`/`GITHUB_ENTERPRISE_TOKEN`) to its child after removing
conflicting token and host variables. These select a host class, not an exact host:
an arbitrary child targeting another host in that class may consume the same
token. The Git HTTPS helper resolves credentials only for its exact host and returns
them directly to Git.

## JSON output

```bash
gh autoaccount status --dir ~/work-project --json
gh autoaccount list --json
```

Each emits one document with `schema_version: 1`. Check the exit status and the
`error` fields before using the result. Both are local-only: no HTTP verification,
scope inspection or `gh auth status`, and no tokens in the output. Invalid
registry/settings or an inaccessible directory can fail the command.

| `status` field | Meaning |
|---|---|
| `directory` | Absolute target directory. |
| `pin` | Object or `null`: `user`, normalized `host`, `token_command_configured`, `directory`, `mode`, and optional `git_name`, `git_email`, `ssh_key`. |
| `environment` | Inherited `profile_user`, `profile_host`, `profile_mode`, and `gh_token_present`, `github_token_present`, `gh_enterprise_token_present`, `github_enterprise_token_present`. |
| `authentication` | `selected`, `source`, `credential_available`, `verified` (always `false`), optional `error`. Availability does not establish validity, login, permissions or SSO. |
| `git` | Optional `git_dir`, `author_ident`, `committer_ident`, `error`: actual Git results in the target directory under the current config and environment, without pin identity injection. |

Authentication sources are `keyring`, `token_command`, `default` and `unavailable`;
inherited token variables never stand in for a selected account. Errors carry `code` and `message`
(for example `credential_unavailable`, `repository_unavailable`,
`identity_unavailable`, with partial results). `list --json` emits `pins` with the
same pin objects, or `[]`.

## Linked worktrees

Use the checkout path for `--dir`. Git-native `includeIf gitdir` matches the actual
Git directory, often under the original repository's `.git/worktrees/`, not the
checkout path, so pin selection and identity includes can differ. Check
`git -C PATH rev-parse --absolute-git-dir`, and repeat both `git var` checks inside
the intended `exec` context. Repository-local `user.name`/`user.email`, command-scoped
config and explicit `GIT_AUTHOR_*`/`GIT_COMMITTER_*` variables override identity
includes.

## Git-native identity files

`setup --git-identity` creates a main include and per-pin fragments under
`gh-autoprofile/git-identity/` in the config directory. Git registers the main
include at the end of its effective global config (respecting `GIT_CONFIG_GLOBAL`);
existing settings and unrelated includes are preserved, and no repository-local
Git config is written. Registration needs Git 2.30+. Identity fields generate
`includeIf "gitdir:..."` rules with directory boundaries: parents appear first, the
deepest pin's supplied fields win, and omitted fields inherit from parent/global
config. Account-only pins do not erase Git-native identity.

Pin, unpin and migration regenerate includes and publish files atomically.
Superseded identity fragments are deliberately retained for Git processes that
still read an older include; unpin removes references, not fragments. Fragments
hold identity values, not credentials. Registry/settings writes are serialized
with `mutation.lock`; a concurrent command fails with a retry message that reports
the lock holder's PID and age. Remove a stale lock only after confirming no writer
is running.
