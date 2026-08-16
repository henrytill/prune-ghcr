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
- the builder and the runtime base both pinned by digest, not only by tag —
  Docker Hub re-pushes a tag like `golang:1.26.6-trixie` on security updates, so
  the tag names different bytes on different days
- the Dockerfile frontend pinned by digest, since `# syntax=docker/dockerfile:1`
  resolves to whatever is newest and the frontend decides how layers and the
  image config are produced
- buildx and buildkit pinned on both workflows, through `version` and a
  `driver-opts: image=moby/buildkit:...@sha256:...` — the SHA on
  `setup-buildx-action` pins the action, which by default installs the newest
  buildx and runs the mutable `moby/buildkit:buildx-stable-1`
- `provenance: false`, since an attestation records the time it was made
- `SOURCE_DATE_EPOCH`, taken from the commit date — the one clock a third party
  rebuilding that commit also has
- buildx's `rewrite-timestamp`, which is why every build passes an `output`
  rather than `push: true` — the latter is the same registry export with no
  attribute to attach this to
- `oci-mediatypes=true` on every export, so that a digest does not depend on
  which exporter produced it

All of these have to be the same at every build: a comparison between two builds
that passed different ones is not a comparison of anything. Most of them are
declared once, in `.github/actions/build-image`, which every build goes through
— publishing, the two determinism builds, the parity build and the rebuild that
verifies a pinned digest. It fixes `provenance: false`, `rewrite-timestamp`,
`oci-mediatypes` and the platforms, which is a digest input as much as the rest,
and it pins the version of `build-push-action` that runs. Callers pass only
where the result goes, the revision to label, and whether the cache may answer.

`SOURCE_DATE_EPOCH` is the exception, and has to be: it comes from a commit only
the caller knows, so each one sets it with `script/source-date-epoch` before
building. The action checks that it is set rather than trusting that, since
`rewrite-timestamp` with no epoch rewrites nothing and produces an
unreproducible image without failing anything.

That is also why the revision is checked there rather than defaulted to the
checkout's SHA: `verify-digest.yml` rebuilds an image built at an earlier commit
and must pass _that_ one, so a default would be right four times and silently
wrong on the one build whose job is to catch exactly this class of mistake.

The pins are what make the promise hold for someone else. Everything a build
resolves by tag is a version two CI builds seconds apart necessarily agree on
and a rebuild months later does not, so pinning is the only way to cover them —
the twice-built job cannot. Bumping one of these pins is expected to change the
digest of unchanged source; that is a normal commit, not a reproducibility
failure.

`SOURCE_DATE_EPOCH` and `rewrite-timestamp` go together. `SOURCE_DATE_EPOCH`
alone fixes the created time in the image config, but the layers `COPY` produces
still carry the build time, and those are what the digest is over.

The last one is insurance against a difference that is easy to miss. Media types
are part of the manifest bytes, so an OCI index and a Docker manifest list over
identical layers hash differently, and a rebuild compared against the published
digest would report a mismatch that is only a difference of spelling.

buildx documents `oci-mediatypes` as defaulting to `true` for `type=oci` and
`false` for the `type=image` exporter that pushes, which would produce exactly
that. In practice the pushed images have been OCI indices regardless — `v2.0.0`,
published before any of this, is one. Naming it on both exporters means the
parity rests on neither the documented default nor the observed one.

Declaring it in one place is still only an intention, so CI checks it: the third
build in `reproducible-image` pushes through the `type=image` exporter to a
throwaway registry service and compares that digest with the `type=oci` one.
Without it, the exporters could diverge in any byte and every outside
reproduction would report a mismatch while CI stayed green.

### Reproducing a published image

The label is a build input, so it has to be the **original** commit, not the
checkout's — and by the **publish, merge, then tag** ordering above, that is not
the release tag. The digest `action.yml` pins at `vX` was built one commit
before the one that repointed `action.yml`, so `git checkout vX` fails to
reproduce for reasons that look exactly like nondeterminism.

Read the commit off the image instead, which is what the revision label is for:

```bash
image=ghcr.io/henrytill/prune-ghcr@<the digest being reproduced>
script/image-revision "$image"
```

Then build that commit:

```bash
git checkout <that revision>
rev=$(git rev-parse HEAD)

# The default docker driver cannot build more than one platform at a time, and
# the buildkit version is a build input, so use the one publish-image.yml pins
# at the commit checked out above rather than the one written here.
docker buildx create --use --driver docker-container \
  --driver-opt image=moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8

# These flags have to match .github/actions/build-image, which is what every
# build in CI goes through. This block is the one copy of them that nothing
# checks, so read them off that file rather than trusting this one: the
# platforms, the provenance, and the two attributes on --output.
SOURCE_DATE_EPOCH=$(script/source-date-epoch "$rev") \
  docker buildx build . \
    --platform linux/amd64,linux/arm64 \
    --label "org.opencontainers.image.revision=${rev}" \
    --provenance=false \
    --output type=oci,dest=./image,tar=false,rewrite-timestamp=true,oci-mediatypes=true

# Against the digest you started from, not against action.yml: at this commit
# action.yml still pins the previous image, which is the whole point of the
# ordering. These are the same scripts verify-digest.yml runs.
built=$(script/layout-digest ./image)
script/assert-digest "${image#*@}" "$built"
```

`--provenance=false`, with the equals sign: the flag takes an optional value, so
a space-separated `false` is parsed as a build context, not as the value.

Reproducing someone else's build from a checkout of a different commit will not
match, and should not: the revision label is what makes a digest auditable back
to a commit.

#### Without Docker

buildx needs a Docker daemon, and podman's builder is not a substitute: the
builder is a build input like any other, so buildah producing different bytes
would say nothing about the image. What works is the same buildkit, run
directly. `buildctl` ships inside the buildkit image, so nothing else has to be
installed:

```bash
# The whole of the section above, restated: this one is its own anchor, and a
# reader who arrives here needs the revision as much as anyone. It is the commit
# the image was built from, which is not the release tag's - read it off the
# image rather than guessing, and check it out before building.
#
# script/image-revision asks the registry with curl and jq, so it needs no
# container runtime at all - which is the point, here of all places.
# Chained: this is meant to be pasted into a shell that has no `set -e`, and
# building with an empty revision is how you get an image that cannot match and
# a mismatch that looks like nondeterminism.
image=ghcr.io/henrytill/prune-ghcr@<the digest being reproduced>
rev=$(script/image-revision "$image") &&
	git checkout "$rev" &&
	epoch=$(script/source-date-epoch "$rev")

# Cleared, not just created: buildctl failing would otherwise leave the previous
# run's layout in place for the comparison below to pass on.
rm -rf out && mkdir -p out

# The buildkit pin publish-image.yml names -- read it with script/builder-pins at
# the commit being reproduced rather than trusting the one written here.
podman run -d --name bk --privileged \
  -v "$PWD":/src:ro -v "$PWD/out":/out \
  docker.io/moby/buildkit:v0.32.2@sha256:28a898719c18a33f4e8000685287fa36fd0dd9560c6440227d3a732d79bb41d8

# buildkitd needs a moment to create its socket, and `podman exec` does not wait.
until podman exec bk buildctl debug workers >/dev/null 2>&1; do sleep 0.5; done

podman exec bk buildctl build \
  --frontend dockerfile.v0 \
  --local context=/src --local dockerfile=/src \
  --opt platform=linux/amd64,linux/arm64 \
  --opt build-arg:SOURCE_DATE_EPOCH="$epoch" \
  --opt label:org.opencontainers.image.revision="$rev" \
  --output type=oci,dest=/out/image,tar=false,rewrite-timestamp=true,oci-mediatypes=true

# Before the comparison, so that a mismatch leaves nothing to clean up by hand:
# the container is not needed to read the result, and a retry would otherwise
# fail on the name being in use.
podman rm -f bk

script/assert-digest "${image#*@}" "$(script/layout-digest ./out/image)"
```

`SOURCE_DATE_EPOCH` is a build argument here rather than an environment
variable, and provenance needs no flag, because `buildctl` does not add an
attestation the way buildx does.

This was run under rootless podman on Debian with nothing but `--privileged`. A
host that confines containers more tightly may want `--device /dev/fuse` and
relaxed seccomp and apparmor, and an SELinux host needs `:z` on both bind mounts
or the context is unreadable. None of that changes the bytes; it only decides
whether the build starts.

This was run against `v2.0.1` and reached the digest `action.yml` pins, which is
worth more than the equivalence looks: buildx and `buildctl` are different
clients, so it also says the pins are what the reproducibility rests on, and not
something `build-push-action` does on the way past.

### The ordering is checked, not just followed

CI builds every pull request twice — second pass with `no-cache`, or the rebuild
replays the first one's layers and compares a result against itself — plus a
third time through the pushing exporter, and fails if the digests differ. That
is what keeps this honest: a single new source of nondeterminism would otherwise
break reproducibility silently and only surface at release time.

Reproducibility is also what lets the ordering be verified rather than followed
correctly. `verify-digest.yml` runs on every pull request and, when the digest
line in `action.yml` differs from the base branch, does this — the steps other
than the rebuild live in `script/verify-digest` and `script/assert-digest`, so
they can be run by hand:

1. Reads the pinned digest `D` out of `action.yml`.
1. Reads `org.opencontainers.image.revision` back off the image at `D` — call it
   `R`, the commit the image claims to be built from.
1. `git diff --quiet R HEAD` over the build inputs: `cmd`, `internal`, `go.mod`,
   `go.sum`, the `Dockerfile`, `publish-image.yml`, where the buildx and
   buildkit pins live, and the `build-image` action, which holds the rest of the
   flags a digest is over. Empty means the image at `R` is an image of the
   source being merged, which is the stale-digest failure caught directly.
1. Rebuilds at this checkout, with `SOURCE_DATE_EPOCH` from `R`'s commit date
   and the revision label set to `R`.
1. Fails unless the rebuilt digest equals `D`.

Step 2 is what makes this possible at all: the image is built from the commit
_before_ the digest commit, so rebuilding with the pull request's own SHA could
never match. `R` is read off the image rather than guessed, and step 3 is what
stops `R` from being a claim the image makes about itself with nothing behind
it.

Step 3 compares against the pull request, not against `main`, so the `main`
ruleset sets `strict_required_status_checks_policy` and a digest pull request
has to be up to date before it can merge. Without that, the check answers a
question that has since gone stale: verify the digest, merge something else that
touches `internal/`, then merge the digest pull request on its old green tick,
and the pin now names an image of source that is no longer `main` — the exact
failure this exists to catch. The setting is load-bearing, not hygiene.

The check belongs on the merge and not on the release tag. The `main` ruleset
has required status checks, so a merge can be gated; the `release tags` ruleset
has only `deletion`, so a tag cannot be — and because tag deletion is blocked, a
tag that failed verification could not even be withdrawn. Recovering would cost
a version number.

`script/release`'s third check — the pinned digest carries the tag being
released — stays as a backstop. It is no longer the only thing standing between
a mis-ordered release and a broken pin.
