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

## Goal: reproducible builds

Making the image bit-reproducible would dissolve the ordering constraint rather
than manage it. If rebuilding the same source yields the same digest, then the
image built before the digest commit and the image built from the digest commit
are the same artifact, and:

- a release tag could rebuild and verify itself, instead of the order being a
  rule people follow
- anyone could rebuild and confirm the published image matches the source, which
  is the real prize

The pieces are mostly in place already — `-trimpath`, a pinned toolchain, a base
image pinned by digest, and `provenance: false`. What is missing is timestamp
determinism: `SOURCE_DATE_EPOCH` with buildx's `rewrite-timestamp`.

It is deliberately not load-bearing today. A single source of nondeterminism
would break it silently, and the failure would surface at release time, so the
ordering rule above stays even once this lands.
