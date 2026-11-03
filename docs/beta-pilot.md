# Public beta pilot

Participation is voluntary. The intended first tag is `v0.1.0-beta.1`; this
document is not evidence that it has been published or tested. Use disposable
workspaces and two accounts already authenticated with GitHub CLI. Native wrapper
mode needs no direnv or `.envrc` on a fresh setup; legacy and export mode do.

## Tasks

1. Record OS/architecture, shell and tool versions without sharing credentials.
2. After the release exists, follow the [installation recipe](../README.md#installation),
   verify the checksum and binary version in an isolated home, and retain any
   working old extension until its replacement has been verified.
3. Review and explicitly approve setup, pins and optional shell or Git integration.
   For an old managed or malformed wrapper `.envrc`, follow the manual cleanup
   instructions in the README before retrying native setup or migration.
4. In two disposable workspaces, follow the [agent checks](agent-shell-support-plan.md).
   Compare remote login and author/committer separately; stop before writes on a
   mismatch. Run from non-interactive shells concurrently and inspect linked
   worktrees' actual Git directories where applicable. In a disposable unpinned
   directory, check that `--require-pin` rejects before running its child; also
   check that a wrong `--expect-user` rejects. Do not expose tokens or switch the
   globally active account for these checks.
5. Optionally repeat in another session and report confusing steps or failures.
   Use repository issues for sanitized functional feedback and [private vulnerability
   reporting](../SECURITY.md) for security concerns. No automatic telemetry or log
   uploads are implied.

## Feedback

Share only what you choose: tag and source, OS/architecture, shell and tool versions,
the first failing step, checksum and version results, expected versus observed
account and author identities (anonymized), and whether you repeated the workflow.
Remove usernames, repository names, private paths, tokens and private keys.
Include whether you reused, skipped or abandoned the workflow in a later session,
and why. Record the first failing step rather than assuming installation succeeded;
do not infer a permission problem from a 404 alone.

## Release checks

Verify the exact resulting public `main` commit with CI and public CodeQL before
tagging. A private-stage CodeQL skip is not a successful scan. Local tests cannot
prove remote CI, a published release, or provenance; verify each separately.
Consider platform gaps, installation friction and unresolved issues before wider
rollout. No adoption or success counts are assumed.
