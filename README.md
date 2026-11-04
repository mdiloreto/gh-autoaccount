# gh-autoaccount

A GitHub CLI extension that routes `gh` and Git HTTPS to a pinned GitHub account per
directory or per repository owner, without `gh auth switch`. Pins apply per command,
so a terminal, an editor task and an agent can work in different accounts at the
same time, and the globally active account never changes. Git author/committer
identity is separate and opt-in.

GitHub CLI [supports multiple accounts](https://github.com/cli/cli/blob/trunk/docs/multiple-accounts.md)
since v2.40.0 but has no automatic per-directory switching. This is beta software.

![Terminal demo of native setup and directory-scoped account selection](docs/demo.gif)

[Watch the MP4](docs/demo.mp4). The demo is offline, using simulated accounts and
API responses; account selection and credential injection are performed by
gh-autoaccount.

## Install

Requirements: GitHub CLI 2.40 or later with your accounts already logged in
(`gh auth login`), and Git.

```bash
gh extension install mdiloreto/gh-autoaccount
gh autoaccount --version
```

This installs a precompiled binary from the latest release. To install a specific
release, add `--pin <tag>` (for example `--pin v0.1.0-beta.3`); GitHub CLI only
installs precompiled extensions from the latest release. Upgrade with
`gh extension upgrade autoaccount`, then run `gh autoaccount setup --migrate` to
refresh generated files. Checksums in `checksums.txt` detect corruption of a manual
download, not publisher identity.

Platforms: native routing, the default, works on Linux and macOS with Bash or zsh.
Windows binaries are published, but native routing is not available there; only the
[deprecated legacy modes](docs/legacy-modes.md) are, and complete Windows shell
integration is not established.

The previous project name, `gh-autoprofile`, was a different extension. Installing
this one does not rename or remove it. Verify the replacement before removing a
working install.

## Quick start

```bash
gh autoaccount setup                 # one time; review the changes it proposes
gh autoaccount default alice         # account used outside any pin
gh autoaccount pin bob --dir ~/work-project
gh autoaccount pin bob --owner work-org
gh autoaccount status                # what applies in the current directory
gh autoaccount doctor                # check the installation
```

Restart the shell (or apply the PATH instructions `setup` prints) so the managed
`gh` shim is first on PATH. Then:

```bash
cd ~/work-project
gh api /user --jq .login              # bob
```

- A **directory pin** applies to the directory and everything below it; the deepest
  pin wins. Add `--git-email` / `--git-name` to a pin for Git identity (see below).
- An **owner pin** routes by the repository owner (organization or user) instead.
  It covers cases a directory pin cannot, such as `git clone
  https://github.com/work-org/x` run from `~`, where Git runs the credential helper
  in the invoking directory.
- The **default** applies where no pin matches. `gh autoaccount default --unset`
  clears it, which also recovers from a missing stored credential. A default is not
  a Git author identity.
- Remove a pin with `gh autoaccount unpin <dir>` or `unpin --owner <owner>`
  (add `--host` for an Enterprise owner pin).

`pin` also accepts `--host` for GitHub Enterprise, `--ssh-key`, and `--token-command`
(a trusted absolute-path credential broker). `gh autoaccount list` (alias `ls`) shows
all pins. All commands: `gh autoaccount --help`.

## How it works

`setup` installs two things, both managed by this tool:

- A **`gh` shim**, a symlink at `~/.local/bin/gh`. When it is first on PATH, plain
  `gh` resolves the pin for the current directory (or the repository owner) and
  injects that account's token into the real `gh` for that one command. No shell
  hook or direnv is needed at runtime, so non-interactive shells are covered:
  agents, editor tasks, cron and scripts. Setup refuses to replace an unmanaged
  file at the shim path.
- A **Git HTTPS credential helper**, which answers Git's credential requests from
  the pin registry for the exact host being accessed and fails closed if the
  selected credential cannot be resolved.

Selection order is: `exec --user`, then the owner pin for the accessed repository,
then the deepest directory pin, then the default. **An owner pin wins over a
directory pin.** The owner comes from Git's request path, `gh -R/--repo`, `GH_REPO`,
the clone target, or the working directory's remotes. For `gh` this is a
best-effort reading of the command line; when the target is unclear, the directory
pin or default applies. Details are in the [routing reference](docs/reference.md).

Credentials are looked up on each invocation; there is no token cache. Headless
keyring access may need a D-Bus session.

### Git identity (optional)

```bash
gh autoaccount setup --git-identity
gh autoaccount pin bob --dir ~/work --git-name "Bob Example" --git-email bob@example.test
git -C ~/work/my-repo var GIT_AUTHOR_IDENT
```

This registers Git `includeIf` rules in your global Git config, generated from pin
identity fields, so author and committer follow the pin without a shell hook or
direnv. It needs Git 2.30+, and Git 2.36+ for identity on owner pins. Repository-local
`user.name`/`user.email` and explicit `GIT_AUTHOR_*`/`GIT_COMMITTER_*` variables
override it. Account selection and identity are separate: a matching author does
not prove which account authenticates a push.

### Agents and scripts

Plain `gh` and Git HTTPS are covered by the shim and helper. For a command that must
not run under the wrong account, use guards:

```bash
gh autoaccount exec --require-pin --expect-user bob --verify-identity -- gh pr status
```

`--require-pin` fails unless a real pin matches (a default does not count),
`--expect-user` checks the selected profile name, and `--verify-identity` checks the
remote login before the child runs. `exec` passes the token to the child and its
descendants; it is not a sandbox. `status --json` and `list --json` give versioned,
local-only output; see the [routing reference](docs/reference.md) for the schema and
a pre-write checklist.

`gh autoaccount skill` prints a bundled guide for coding agents. Install it under an
explicit skills directory (Linux and macOS only; it never overwrites a customized
file):

```bash
gh autoaccount skill install --dir .claude/skills    # or .agents/skills, .opencode/skills
```

### Externally managed shells

`gh autoaccount setup --no-shell-rc` leaves shell RC files and PATH untouched and
persists that choice for later `setup`, `--migrate` and `doctor --fix`. You then put
the shim directory before the real `gh` on PATH yourself. `status` and `doctor`
report this as not verified, since a subprocess cannot inspect the parent shell.
`setup --no-shell-rc=false` restores automatic integration. An optional
`setup --alias` configures a `gh aa` alias for `autoaccount`; it never overwrites an
existing alias.

## Using with mise

If mise manages `gh` (for example `gh = "latest"`), `mise activate` puts mise's tool
directories ahead of `~/.local/bin` on each prompt, so plain `gh` runs mise's binary
and bypasses the managed shim. `doctor` and `status` warn when `gh` does not resolve
to a managed shim. Fix it with:

```bash
gh autoaccount setup --mise    # requires native routing and mise on PATH
```

This installs a second shim in a directory that holds only `gh`
(`$XDG_DATA_HOME/gh-autoaccount/bin`, default `~/.local/share/gh-autoaccount/bin`)
and adds that directory to `env._.path` in mise's global config via
`mise config set -g --append env._.path <dir>`. mise puts those entries before its
tool paths without reordering other tools. It is persisted, so `setup --migrate` and
`doctor --fix` re-apply it. Undo it with `gh autoaccount setup --mise=false`, which
removes that one entry and the shim; if `mise` is not on PATH at that point it only
warns and leaves the entry in mise's config for you to remove. The shim still runs
the first other `gh` on the PATH it was started with, so the version mise selects,
including a project pin, keeps applying. An explicit `GH_PATH` or
`GH_AUTOPROFILE_REAL_GH` overrides that choice.

Setup recognizes only the exact absolute directory it writes, so remove any
hand-written alternate spelling (`~/...`, a trailing slash) yourself. If a
`--mise=false` fails after unregistering, rollback re-adds the entry at the end of
`env._.path`, not at its original position, because mise's CLI cannot insert at a
position. `setup --mise` needs an existing global mise config that mise trusts; if it
fails with "not trusted", create the config or run `mise trust`, then retry.

## Troubleshooting

```bash
gh autoaccount doctor          # report problems
gh autoaccount doctor --fix    # runs setup --migrate; it changes configuration, so review first
gh autoaccount status          # local view of what applies here
```

`doctor` reports library and hook health, the `gh` shim and PATH, Git credential
helper configuration, Git identity registration, persisted external-shell mode, pin
schema, and managed `.envrc` permissions. Common warnings:

- **Plain `gh` resolves to something other than the managed shim**: the shim
  directory is not first on PATH. Restart the shell or fix PATH; with mise, see above.
- **Git credential helper not configured, or its target is missing**: run
  `gh autoaccount setup` again (or `setup --migrate` after an upgrade, which repoints
  the shim and helper at the installed binary).
- **`useHttpPath` is not true**: only reported for hosts with owner pins; `doctor
  --fix` restores it.
- **Pins missing a mode, or `.envrc` files not 0600**: `doctor --fix`.
- **Setup, pin or unpin refuses because of an old `.envrc` block**: a managed or
  malformed wrapper block from legacy mode blocks native commands; follow the
  cleanup steps in [legacy modes](docs/legacy-modes.md#migrating-from-legacy-wrapper-blocks).
- **A command used the wrong account**: run `gh autoaccount status --dir <dir>`.
  Without a shim or helper, ordinary global `gh`/Git behavior applies. A 404 can also
  mean missing access or SSO rather than the wrong login.

When reporting an issue, include OS, shell, `gh` version and sanitized `doctor`
output. Never include tokens or private keys.

## Files and the `gh-autoprofile` name

The project was previously named `gh-autoprofile`, and its on-disk names are kept for
compatibility. Config and state live under `~/.config/gh-autoprofile/`
(`$XDG_CONFIG_HOME/gh-autoprofile/` if set): `pins.yml`, `settings.yml`, the hook,
the credential helper script and optional `git-identity/` includes. The `gh` shim is
`~/.local/bin/gh`. `GH_AUTOPROFILE_*` environment variables, `.envrc` markers and
`use_gh_autoprofile` also keep their names. This is not an incomplete migration;
do not bulk-rename these. The registry is written with mode 0600 and the config
directory with mode 0700.

## Security

Pins are the only source of accounts. Tokens are injected per command into the
child process, not exported into your shell, and repository content never selects
credentials. This limits exposure but is not a sandbox: a malicious child or
same-user process can still read what it is handed or replace trusted paths.
Environment variables select a host class (`github.com`/`*.ghe.com` versus other
Enterprise hosts), not an exact host; the Git helper enforces the exact host. A
checkout's remotes or submodules under an owner-pinned owner authenticate as that
pin's account. Export mode, which places tokens in the environment, is deprecated.
See [SECURITY.md](SECURITY.md) for trust boundaries and private vulnerability
reporting.

## Deprecated modes

Legacy wrapper mode (direnv plus shell-prompt functions) and export mode
(`pin --export-token`, tokens in the environment) are deprecated. They miss
non-interactive shells and export mode exposes tokens to every child of the shell.
Fresh installs use native routing. To move an existing installation, run
`gh autoaccount setup --native`. See [docs/legacy-modes.md](docs/legacy-modes.md).

## Contributing

Development requires Go 1.26 or newer:

```bash
git clone https://github.com/mdiloreto/gh-autoaccount
go build -o bin/gh-autoaccount ./cmd/gh-autoaccount
go test ./...
```

See [offline testing](docs/testing.md) for the acceptance suite and its limits.
Functional feedback belongs in repository issues; report vulnerabilities privately
as described in [SECURITY.md](SECURITY.md).

## License

MIT; see [LICENSE](LICENSE).
