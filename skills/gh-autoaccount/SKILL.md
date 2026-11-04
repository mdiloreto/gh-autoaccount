---
name: gh-autoaccount
description: Use when routing GitHub accounts by directory with gh-autoaccount, guarding gh-autoaccount exec for agents, or recovering a gh-autoaccount pin or missing credential.
---

# gh-autoaccount

Route GitHub commands by directory without switching the globally active account.
Use the installed executable's help as the authority for its version.

## Boundaries first

- Reading this skill does not authorize changes. Get user approval for setup, pin,
  unpin, default changes, `doctor --fix`, `setup --migrate`, and skill installation.
  Native setup is explicit, not automatic; do not use `gh auth switch` to fix
  directory routing. Approve network access and remote writes separately.
- Never print tokens, dump the environment, read authentication stores, or export
  ambient `GH_TOKEN`/`GITHUB_TOKEN`. Treat external token commands as trusted code:
  inspect their use and obtain approval before executing them.
- `exec` passes credentials to the child and its descendants; it is not a sandbox.
  Native helpers do not universally scrub credentials already inherited by a process.
  Host-class token variables do not guarantee exact-host isolation.
- Do not weaken or remove a failed guard to make a command succeed. Stop, diagnose,
  and obtain approval for any proposed changes.

## Inspect before acting

Read the installed `gh autoaccount --help`, `exec --help`, `status --help`, and
`list --help` first. In the intended working directory, inspect:

```bash
gh autoaccount status --json
gh autoaccount list --json
gh autoaccount doctor
```

These are local diagnostics, not remote identity or access verification. Inspect
errors even if status succeeds; credential availability is not proof of validity.
Use `doctor` without `--fix` for inspection. `status` can perform local credential
lookup, including invoking a configured token command; assess its trust first.
For another directory use `status --dir PATH --json` and inspect its context.

## Select and verify

Without `--require-pin`, `exec` selects an explicit `--user`, then the deepest
real matching directory pin, then the `github.com` default, then unselected legacy
behavior. An explicit `--user` without `--require-pin`, and a default, do not
inherit pin host, token command, identity, or SSH metadata, even when a pin matches.
Pins match canonical physical paths.

`--require-pin` requires a real pin, retains its metadata, and rejects a conflicting
`--user`; a default is not a pin. `--expect-user` compares the selected profile,
not the credential's remote identity. With approval for a network read and a trusted
child, verify the intended pin and login (replace `bob` with the intended user):

```bash
gh autoaccount exec --require-pin --expect-user bob --verify-identity -- gh api /user --jq .login
```

`--verify-identity` remotely checks the login before starting the child; matching
login does not prove repository authorization. `--dir PATH` selects both the pin
lookup directory and the child's working directory. Check exit status, keep the same
directory and guards for subsequent approved work, and stop on mismatch.

Git authentication is separate from author and committer identity. Inspect both
`git var GIT_AUTHOR_IDENT` and `git var GIT_COMMITTER_IDENT` in the intended context,
and read relevant Git config when needed; do not automatically set identity. Linked
worktrees may have a Git directory outside the checkout and different include rules.

## Recovery

A 404 can mean the wrong repository or account, missing access, or SSO issues; it is
not conclusive. Inspect local status and, with approved network access, verify the
identity using the guarded command above. Do not silently switch global accounts.
The native literal `gh autoaccount` management path supports recovery when the
selected credential is missing; it still requires a trusted installed extension
path and a valid real `gh` binary. Unmodified upstream `gh` may require login before
extension dispatch. Do not assume arbitrary aliases or `gh extension exec` have
the native recovery behavior.

Managed or malformed old wrapper `.envrc` content blocks native setup, pin, unpin,
and migration. With separate approval, review the entire file, manually run
`direnv deny`, remove only managed bytes while preserving custom content, then reload
the shell and clear old exports before retrying. Do not automate cleanup or assume
backups exist. Preserve the compatibility storage `~/.config/gh-autoprofile` and
`GH_AUTOPROFILE_*` names; do not broadly rename them.

## Skill access

The skill command itself reads no authentication or account configuration and
needs no setup; invoke the binary directly or use the native literal management
path for credential-free recovery. Unmodified upstream `gh` may require login
before dispatching `gh autoaccount skill`. An
explicitly approved `gh autoaccount skill install --dir <skills-parent>` writes
only under the selected destination, without changing Git, shell, or account config.
Automatic installation supports Linux and macOS only; other platforms fail before
filesystem changes. The printed guide is portable and can be placed manually on
Windows, though automatic installation there is not tested or supported. On supported
platforms it creates `gh-autoaccount/SKILL.md`; an identical regular file is a no-op,
while a customized or non-regular file and managed-child or final symlinks are refused.
The explicitly selected parent and its ancestors may themselves be symlinks. No
default destination or force overwrite is provided; do not install automatically.
