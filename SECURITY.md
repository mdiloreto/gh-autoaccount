# Security Policy

## Threat model

gh-autoaccount defends one boundary above all: content you did not write
(cloned repositories, `.envrc` fragments, hijacked binaries on `PATH`) must
not gain access to the wrong stored GitHub credential or silently act as the
wrong account.

In scope — a fix is expected if broken:
- detected unmanaged `.envrc` content requires manual review and `direnv allow`
- `pin` and `setup --migrate` skip auto-allow when the directory or original or
  rewritten `.envrc` is group/other-writable, or cannot be inspected
- automatic authorization uses direnv 2.38.2's private allow format to bind the
  entry to generated, inspected bytes; unsupported platforms, versions and
  filesystems and configurations, existing deny entries, ACLs, legacy stores
  and required files need manual review
- wrapper-mode tokens live only in child processes, never in the parent shell
- generated shims, hooks, and helper scripts quote interpolated paths literally
- account activation fails closed instead of falling back to another account
- pins are the only source of accounts: canonical physical directory pins and
  `(host, user)` select credentials. With owner pins, the repository URL being
  accessed (credential path, `-R`/`GH_REPO`, clone target, or a checkout's remotes
  and submodules) chooses among them, and credentials are returned only for the
  pin's exact host; a checkout's submodule under an owner-pinned organization
  therefore fetches as that account. Git HTTPS, SSH, and commit identity are
  separate boundaries
- native `gh` receives only the selected host class's primary token variable;
  explicit `exec` opts its child into that class's primary/fallback pair after
  conflicting token and host variables are removed. These variables are scoped by
  GitHub host class, not by one exact hostname. `--verify-identity` checks the
  selected host's remote principal when requested; `--expect-user` alone checks
  only the selected profile
- parsed third-party output (e.g. `gh auth status`) is sanitized before display
- a fatal native/legacy setup transition after an RC edit restores the actual
  RC target and the pre-transition managed assets, settings, and helper chain;
  an RC symlink is not replaced

Out of scope — accepted trust boundaries:
- startup executable resolution. An externally supplied absolute `GH_PATH` is
  authoritative; otherwise initial discovery uses `PATH`. Keep both trusted.
  Setup-generated helper, shim, and direnv-library surfaces embed the resolved
  absolute real-`gh` and extension paths and override hostile inherited routing
  markers instead of rediscovering through a project-controlled `PATH`.
- The shell hook pins `gh`/`git` and supported shebang interpreters when sourced,
  before project PATH changes. It unwraps the generated gh shim without evaluating
  its contents. Simple `env name`, `env -S name`, and `env --split-string=name`
  shebangs and absolute native interpreters without arguments are supported;
  ambiguous/unsupported forms fail closed in wrapper mode. Start a fresh trusted
  shell after changing executables. Executable files, their interpreter paths,
  script bodies, startup files and runtime dependencies must remain trusted;
  this is not a sandbox for arbitrary third-party wrappers.
- Auto-allow is a convenience for trusted, single-user checkouts. Its content
  binding does not lock the checkout or prevent direnv's own subsequent
  check-to-execution race. Same-UID processes can directly alter direnv state.
  Checks cannot eliminate concurrent same-UID mutation or account for every
  filesystem-specific write grant.
  Do not use automatic authorization in a checkout with concurrent untrusted
  writers, even if its local mode bits pass. Review and allow manually instead.
- Canonical path matching is a routing rule, not a lock. It does not prevent a
  same-UID process from replacing a path or symlink between inspection and use.
- Absolute generated routes are likewise not immutable: a same-UID process can
  replace the selected executable or one of its runtime dependencies after setup.
- your home directory: `pins.yml` (0600) and config files are trusted input.
  A user who can write them is you.
- A configured `token_command` is a trusted absolute executable, run without a
  shell per command with sanitized credential variables, a timeout, and bounded
  single-token output. It is authoritative and does not fall back after failure.
  Its executable and dependencies are not sandboxed.
- Export mode is explicit compatibility behavior: tokens are exposed to the shell
  environment and its children. Command-scoped injection is not a sandbox either.
- GitHub infrastructure, the `gh` keyring, and TLS to the selected GitHub host.

## Release artifacts

The release workflow pins third-party Actions by commit, runs tidy,
tests, vet, build, and `govulncheck` before GoReleaser, and publishes checksums and
per-binary SBOMs. Provenance attestation is attempted only for public repositories;
its success must be verified for each release. A checksum
downloaded from the same release detects corruption but is not an independent
signature or proof of reproducible compilation.

## Supported versions

Until gh-autoaccount reaches 1.0, only the latest published release receives
security fixes. Upgrade before reporting a problem that affects an older
version.

## Reporting a vulnerability

Please use [GitHub private vulnerability reporting](https://github.com/mdiloreto/gh-autoaccount/security/advisories/new).
Do not open a public issue for a vulnerability that has not been fixed.

Include the affected version, operating system, reproduction steps, expected
impact, and any suggested mitigation. Do not include real access tokens,
private keys, or other credentials.

You should receive an acknowledgement within seven days. Fix timing depends on
severity and reproducibility; confirmed vulnerabilities will be coordinated
privately until a patched release is available.
