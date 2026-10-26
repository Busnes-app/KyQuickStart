# Verifying releases

[Overview](../README.md) · [Install a release](../README.md#install-a-release) · [Assurance case](ASSURANCE-CASE.md)

A KyQuickStart release `vX.Y.Z` (or `vX.Y.Z.N`, a daily dependency release) is two things, both
built by `.github/workflows/release-image.yml` from the tagged commit:

- four `kyquickstart` binaries on the GitHub release: `kyquickstart-X.Y.Z-linux-amd64`,
  `-linux-arm64`, `-darwin-amd64` and `-darwin-arm64`;
- the suite runner image `ghcr.io/busnes-app/kyquickstart-runner:X.Y.Z`, which KyYard runs. Each
  binary carries that image's digest (`version --runner-image`) and installs KyYard only with it.

This page shows how to check that a binary and its runner image were built by this repository's
release workflow from the tagged commit, and how to rebuild the binaries yourself and get the
same bytes.

Read the [release notes](../CHANGELOG.md) for changes, upgrade impact and artifact
availability. A valid attestation does not replace those checks: v0.4.0, for example,
published its runner image but uploaded no installer binaries.

## What is signed, and by whom

- The release workflow publishes a SLSA build-provenance attestation, through GitHub's Sigstore
  integration, for the image digest (`publish` job) and for each of the four binaries
  (`binaries` job). It verifies each attestation before it uploads the binaries and before it
  moves `:stable`, and checks the uploaded binaries again after downloading them.
- There is no long-lived signing key to download. The signature is tied to a workflow identity,
  and that identity is what you check:
  `https://github.com/Busnes-app/KyQuickStart/.github/workflows/release-image.yml@refs/tags/v<version>`.
  `gh` fetches Sigstore's public trust root itself.
- The attestation records the commit it was built from. The commands below compare it with the
  commit the release tag points at.
- The image also carries an SPDX software bill of materials made by BuildKit. The SBOM sits in
  the image index and is **not** separately signed; trust it as far as you trust the digest you
  verified. The binaries have no separate SBOM; `go version -m <binary>` lists the modules
  compiled in.

## Verify a release binary and its runner image

You need `gh`, logged in. Name the version and your platform yourself:

```sh
v=<release version, e.g. 0.4.1>; p=<os>-<arch, e.g. linux-amd64>
f=kyquickstart-$v-$p
id=https://github.com/Busnes-app/KyQuickStart/.github/workflows/release-image.yml@refs/tags/v$v
jq_commit='.[0].verificationResult.statement.predicate.buildDefinition.resolvedDependencies[0].digest.gitCommit'
gh release download "v$v" --repo Busnes-app/KyQuickStart --pattern "$f" \
  && sha=$(gh api "repos/Busnes-app/KyQuickStart/commits/refs/tags/v$v" --jq .sha) \
  && [ "$(gh attestation verify "$f" --repo Busnes-app/KyQuickStart --cert-identity "$id" \
       --format json --jq "$jq_commit")" = "$sha" ] \
  && echo "OK: $f is release v$v, built from $sha"
```

Then check the runner image that binary pins. The pin is read from the bytes, so this works for a
binary built for another platform too:

```sh
pin=$(grep -aoE "ghcr\.io/busnes-app/kyquickstart-runner:$v@sha256:[0-9a-f]{64}" "$f" | head -n 1)
[ "$(gh attestation verify "oci://ghcr.io/busnes-app/kyquickstart-runner@${pin#*@}" --repo Busnes-app/KyQuickStart \
     --cert-identity "$id" --format json --jq "$jq_commit")" = "$sha" ] \
  && echo "OK: $pin is release v$v, built from $sha"
```

Any failure means: do not run that binary. A changed byte, a different workflow, a branch
instead of the tag, or another repository each fail the check. On macOS, clear the quarantine
flag only after the check passes ([README](../README.md#install-a-release)).

To read the runner image's SBOM (needs `docker` with buildx):

```sh
docker buildx imagetools inspect "ghcr.io/busnes-app/kyquickstart-runner@${pin#*@}" --format '{{ json .SBOM }}'
```

## Rebuild the binaries and compare

`scripts/release-binaries.sh` is the release build: the workflow runs it on the tagged checkout
with the release's version and the runner image digest it just verified. You run it the same way,
with the Go version the release used, which the binary itself records.

```sh
v=<release version>; f=kyquickstart-$v-<os>-<arch>   # the binary you downloaded and verified
pin=$(grep -aoE "ghcr\.io/busnes-app/kyquickstart-runner:$v@sha256:[0-9a-f]{64}" "$f" | head -n 1)
go_version=$(go version "$f" | awk '{print $2}')
git clone --branch "v$v" https://github.com/Busnes-app/KyQuickStart.git kyquickstart-v$v
(cd kyquickstart-v$v && GOTOOLCHAIN=$go_version sh scripts/release-binaries.sh "$v" "$pin" ../rebuilt-v$v)
sha256sum "$f" rebuilt-v$v/"$f"
```

The two hashes must match. Run this with no other `GO*` variables set (`GOAMD64`, for one,
changes the code); the script clears `GOFLAGS` itself. `GOTOOLCHAIN` downloads the exact Go
release, which needs Go 1.21 or later on your machine.

That recipe needs a release built with `-buildvcs=false` (the clone's `scripts/release-binaries.sh`
contains it). Those binaries depend only on the source, the Go version, the version string and
the pin: not on the directory, a `.git` directory or anything untracked. CI builds them from the
checkout and from a `git archive` copy in another directory on every push and requires identical
bytes (the `release binaries are repeatable` step). No release has been published with this yet,
so the recipe has been checked on `master` but not against a published release.

### Releases built before `-buildvcs=false` (v0.4.1 and earlier)

These binaries record the build checkout's git state, so the rebuild must repeat the release's
checkout exactly: a fresh, unmodified clone of the tag that is not inside another git working
tree, with the output directory `dist` inside it. Writing `dist` there is what the release did,
and it is why three of the four binaries say `vcs.modified=true`.

```sh
git clone --branch "v$v" https://github.com/Busnes-app/KyQuickStart.git kyquickstart-v$v
(cd kyquickstart-v$v && GOTOOLCHAIN=$go_version sh scripts/release-binaries.sh "$v" "$pin" dist)
sha256sum "$f" kyquickstart-v$v/dist/"$f"
```

This reproduced all four v0.4.1 binaries byte for byte (Go 1.26.6). v0.4.0 has not been tried.

The runner image's own binary is built by the `Dockerfile` without the runner pin, so it is not
one of these four; the attestation is how you check the image.
