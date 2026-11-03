# Offline acceptance testing

On Linux amd64 or arm64, pre-fetch the Go module dependencies from the repository,
using the same PATH and environment (especially GOPATH and GOMODCACHE) as the runner.
This download requires network access if the dependencies are not already cached.
Then run the suite (the script itself also works from another directory):

```bash
GOENV=off GOWORK=off GOTOOLCHAIN=local go mod download
bash scripts/test-offline.sh
```

Prerequisites: a compatible local Go toolchain, Git, the real GitHub CLI (`gh`),
Bash, zsh, jq, and standard Linux command-line utilities, including
`/usr/bin/git`, `/usr/bin/bash`, `/usr/bin/env`, and `/usr/bin/jq` as used by
the smoke fixtures (an Ubuntu-like system). The fixtures support Linux amd64
and arm64; earlier runtime validation was Linux amd64 only, not arm64. The combined
candidate must still be validated after integration. The runner logs the
selected gh version. The runner prefers `/usr/bin/gh` when available, otherwise
it searches PATH. Set `GH_AUTOPROFILE_TEST_GH=/absolute/path/to/real/gh` to
override that choice; do not point it at an installed managed shim. The
version check runs in an isolated environment but is not proof that a forwarding
shim is the real CLI. CI uses `/usr/bin/gh` and logs its version; the
Ubuntu image and apt package are not a pinned, bit-reproducible toolchain.

The runner builds one candidate in a temporary directory, runs uncached launcher
Go tests, then passes that candidate to the concurrent agent, native routing, and
onboarding smoke tests. The onboarding test uses a real gh extension install and
dispatch in an isolated local home, with dummy stored accounts. Fixtures exercise
native setup, defaults, pins, explicit selection, missing-credential recovery and
migration. The agent and native tests use their own local provider fixtures.

The runner disables Go module downloads, checksum lookup, and automatic toolchain
downloads, and isolates home, XDG and Git configuration, credentials and D-Bus
session environment. Its proxy settings discourage network access; they are not
an OS network sandbox. With the same tools and fixtures, assertions are intended
to be repeatable, not bit-for-bit build artifacts. This suite does not prove live
GitHub authentication, published releases, network isolation or real keyring
integration. Run it twice in independent scratch directories when checking
repeatability.

## Terminal demo

Populate the Go module cache once before offline verification, then verify the
routing scenario and regenerate the GIF and MP4 from the repository root:

```bash
go mod download
bash demo/record.sh --check
bash demo/record.sh
```

Dependencies: Go, Bash, Git, and Python 3. Recording also needs VHS 0.12.1,
ttyd 1.7.7, ffmpeg, and Chromium; `--check` does not need those recording tools.
The recorder does not automatically install tools or change global configuration.
Its simulated accounts and API responses do not prove live network authentication.
