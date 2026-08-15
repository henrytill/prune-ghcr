# Releasing

The action runs from a prebuilt image referenced by digest, so releasing is not
just moving a tag. This document is the reasoning; `script/release` enforces the
parts it can.

## Why a digest

`uses: henrytill/prune-ghcr@<sha>` should pin the code that runs. Against a
mutable tag it would pin only `action.yml`, and a consumer following the usual
supply-chain advice would silently pick up whatever was pushed to that tag last.

This is not theoretical. During the port, a temporary trigger republished
`:go-port` on every push: the tag moved to a newer image while the digest in
`action.yml` stayed put and kept working. That is the property being bought.

## The order

`action.yml` has to name the digest of an image built from the code it ships
with, which looks circular. It is not, because **`action.yml` is not a build
input**: the image depends only on `cmd/`, `internal/`, `go.mod`, `go.sum` and
the `Dockerfile`. A commit that changes only the digest line produces the same
image content.

So:

1. Dispatch **Publish Image** with the version, from the branch being released.
   It builds, pushes `ghcr.io/henrytill/prune-ghcr:vX.Y.Z`, and opens a pull
   request repointing `action.yml` at the digest.
1. Merge that pull request.
1. Run `script/release` and give it the same version. It verifies before
   tagging.

Publishing cannot be triggered by the release tag. An image built from the tag
would be referenced by nothing: `action.yml` at that tag was written earlier and
names an older digest.

## What script/release checks

GitHub cannot gate a tag on a status check — required status checks are a
branch-target rule, and the tag ruleset only forbids deletion — so this is the
only place the ordering can be enforced:

- `action.yml` references an image by digest at all
- that digest belongs to a version that was actually published
- that version carries the tag being released

The third is what catches a stale digest, which is the failure that would
otherwise ship older code under a newer version.

## Do not prune this package carelessly

Every released image must keep its version tag. An untagged version is exactly
what this action deletes, so pruning this package would delete the images that
older releases pin — breaking every consumer pinned to them, while leaving the
current release fine.

Running the action against its own package is safe only for versions no release
references.

## Reproducible builds

The image is bit-reproducible: rebuilding a commit yields the digest that commit
published. That makes the image built before the digest commit and the image
built from the digest commit the same artifact, and it means anyone can rebuild
and confirm the published image matches the source, which is the real prize.

What gets it there:

- `-trimpath` and `CGO_ENABLED=0`, so the Go binary does not embed build paths
- a pinned toolchain and a base image pinned by digest
- `provenance: false`, since an attestation records the time it was made
- `SOURCE_DATE_EPOCH`, taken from the commit date — the one clock a third party
  rebuilding that commit also has
- buildx's `rewrite-timestamp`, which is why `publish-image.yml` passes
  `outputs: type=image,push=true,...` rather than `push: true`
- `oci-mediatypes=true` on every export, so that a digest does not depend on
  which exporter produced it

The middle two go together. `SOURCE_DATE_EPOCH` alone fixes the created time in
the image config, but the layers `COPY` produces still carry the build time, and
those are what the digest is over.

The last one is insurance against a difference that is easy to miss. Media types
are part of the manifest bytes, so an OCI index and a Docker manifest list over
identical layers hash differently, and a rebuild compared against the published
digest would report a mismatch that is only a difference of spelling.

buildx documents `oci-mediatypes` as defaulting to `true` for `type=oci` and
`false` for the `type=image` exporter that pushes, which would produce exactly
that. In practice the pushed images have been OCI indices regardless — `v2.0.0`,
published before any of this, is one. Naming it on both exporters means the
parity rests on neither the documented default nor the observed one.

### Reproducing a published image

The label is a build input, so it has to be the **original** commit, not the
checkout's:

```bash
git checkout <the commit being reproduced>
rev=$(git rev-parse HEAD)

# The default docker driver cannot build more than one platform at a time.
docker buildx create --use --driver docker-container

SOURCE_DATE_EPOCH=$(git log -1 --pretty=%ct "$rev") \
  docker buildx build . \
    --platform linux/amd64,linux/arm64 \
    --label "org.opencontainers.image.revision=${rev}" \
    --provenance=false \
    --output type=oci,dest=./image,tar=false,rewrite-timestamp=true,oci-mediatypes=true

jq -r '.manifests[0].digest' image/index.json   # compare with action.yml
```

`--provenance=false`, with the equals sign: the flag takes an optional value, so
a space-separated `false` is parsed as a build context, not as the value.

Reproducing someone else's build from a checkout of a different commit will not
match, and should not: the revision label is what makes a digest auditable back
to a commit.

### The ordering rule stays

CI builds every pull request twice — second pass with `no-cache`, or the rebuild
replays the first one's layers and compares a result against itself — and fails
if the digests differ. That is what keeps this honest: a single new source of
nondeterminism would otherwise break reproducibility silently and only surface
at release time.

But the release still does not verify itself. Reproducibility could in principle
dissolve the ordering constraint rather than manage it, by having a release tag
rebuild and check its own digest. Until something actually does that, **publish,
merge, then tag** remains a rule people follow, and `script/release` remains the
only thing enforcing it.
