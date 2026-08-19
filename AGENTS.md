# AGENTS.md

This file provides guidance to coding agents working in this repository, and is
the only tracked copy of it. `CLAUDE.md` is ignored rather than tracked, so a
clone starts without it; recreate the link with `ln -s AGENTS.md CLAUDE.md` for
an agent that looks for that name.

## Overview

A GitHub Action, written in Go, that deletes untagged GHCR container versions
while keeping any that a tagged multi-arch index still references.

Ported from a TypeScript action, which was itself ported from the composite
action + `prune_ghcr.py` at
`henrytill/devcontainer-debian/.github/actions/prune-ghcr`. Nothing of the
TypeScript remains; why `go-github` and `go-containerregistry` are used rather
than the standard library is recorded in those two package comments.

## Commands

```bash
go build ./...
go test ./...          # add -race in CI
gofmt -l cmd internal  # prints files needing formatting; empty is good
```

Static analysis, at the version CI pins:

```bash
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
```

Single test file / test name:

```bash
go test ./internal/prune/
go test ./internal/prune/ -run TestSkipsVersionsYoungerThanMinAge
```

Run the action locally against a `.env` file (copy from `.env.example`):

```bash
env $(grep -vE '^\s*#|^\s*$' .env | xargs) \
  GITHUB_OUTPUT=/dev/null go run ./cmd/prune-ghcr
```

Inputs arrive as `INPUT_<NAME>` with hyphens **preserved**, not converted to
underscores. `env` is required rather than sourcing the file: the shell cannot
assign a name like `INPUT_MIN-AGE-HOURS`, and sourcing drops it silently.

## Architecture

- `cmd/prune-ghcr/main.go` — reads and validates inputs, constructs the clients,
  calls `Prune`, sets outputs. `run` returns an error and `main` turns it into
  one `::error::` and a non-zero exit. It owns all Actions-runtime concerns so
  nothing below it does.
- `internal/prune` — the algorithm, and the only package worth reading to
  understand the action. It declares the `Versions`, `ManifestReader` and
  `Logger` interfaces it depends on, so the tests drive it with fakes and never
  touch the network.
- `internal/api` — packages REST API over `go-github`. Selects the user or
  organization endpoints from `Target.UserOwned`; `/user/packages/...` is the
  only path that can delete versions of a user-owned package. Converts
  `github.PackageVersion` into its own `ContainerVersion`, which keeps the
  pointer dereferencing in one place and keeps `prune` free of go-github.
- `internal/registry` — manifest reads over `go-containerregistry`. `remote.Get`
  negotiates media types and verifies that content hashes to the digest
  requested. Registry repository paths must be lowercased even when the GitHub
  owner or package is not.
- `internal/actions` — the `@actions/core` surface the action uses. Workflow
  commands go to stdout; `SetOutput` appends to `$GITHUB_OUTPUT` in heredoc
  form. Only the property-less command form is implemented, which avoids a
  second escaping table covering `:` and `,`.
- `internal/retry` — linear backoff. Return a `NonRetryableError`, or build the
  error with `retry.NewStatusError`, for permanent failures: retrying a 403 or
  404 just burns the backoff, and the tests depend on those failing immediately.
  `Do` takes a `warn func(string)` rather than importing `internal/actions`.

### Invariants worth preserving

- A tagged manifest that cannot be read aborts the whole run. Its children would
  otherwise look untagged, and deleting them breaks a live tag.
- A version with no usable `updated_at` is skipped, not deleted. The zero time
  is older than any cutoff, so treating it as data would delete exactly the
  versions we know least about.
- Individual delete failures are counted and the run continues, then the run
  fails at the end.
- The token has all whitespace stripped, and an empty token is a hard failure
  rather than a no-op, so a misconfigured secret cannot leave a green run that
  quietly stopped pruning.

## Packaging

`action.yml` is a Docker action referencing a prebuilt image **by digest**:

```yaml
runs:
  using: docker
  image: docker://ghcr.io/henrytill/prune-ghcr@sha256:...
```

The digest is the point. `uses: henrytill/prune-ghcr@<sha>` should pin the code
that runs, and against a mutable tag it would pin only `action.yml`. The cost is
ordering: **publish, merge, then tag**. `Publish Image` is dispatch-only and
pushes a `publish/<tag>-<run id>` branch that repoints `action.yml` — opening
the pull request is a manual step, because this repository does not let Actions
open pull requests; `script/release` verifies the digest is published under the
tag being released.

`docs/releasing.md` has the reasoning, including why a release tag cannot
trigger the publish and why pruning this package would break older releases.

The image is bit-reproducible, via `SOURCE_DATE_EPOCH` and buildx's
`rewrite-timestamp`, and `ci.yml` builds every pull request twice — plus a third
time through the exporter publishing uses — to keep it that way. Any change to
the build has to preserve that: nondeterminism would otherwise go unnoticed
until a release. `verify-digest.yml` then checks, on the pull request that
repoints `action.yml`, that the digest it pins rebuilds from the source being
merged; it is a required check, and it depends on the `main` ruleset's
`strict_required_status_checks_policy`, because it compares against the pull
request rather than against `main`.

What CI cannot check, pinning has to cover. Both of its builds resolve the same
Dockerfile frontend, builder image, buildx and buildkit, so all four are pinned
by digest — in the `# syntax=` and `FROM` lines, and on `setup-buildx-action`,
where the action SHA pins the action and nothing else.

Everything that is a build input lives in exactly one place, and the rest is
read from it:

- the buildx and buildkit pins in `publish-image.yml`, read by `ci.yml` and
  `verify-digest.yml` through `script/builder-pins`
- the platforms, `provenance`, `rewrite-timestamp`, `oci-mediatypes` and the
  `build-push-action` version in `.github/actions/build-image`, which every
  build goes through
- `SOURCE_DATE_EPOCH` is the exception: each caller derives it with
  `script/source-date-epoch`, since it comes from a commit only the caller
  knows, and the action checks it is set

Anything that decides the output bytes also has to be on `build_inputs` in
`script/verify-digest`, or a change to it between publishing and merging fails
verification on an image that reproduces perfectly.

Consequences: Linux runners only, permanently, including self-hosted macOS and
Windows. The image must stay public, since a Docker action cannot authenticate a
private pull. The container runs as **root** so `$GITHUB_OUTPUT` stays writable
— that is why the non-root findings from checkov and trivy are suppressed rather
than fixed.

## CI

Static analysis has one owner: `golangci-lint`, configured by `.golangci.yml`,
run from `ci.yml`. Do not add `go vet` or `staticcheck` steps — the standard set
already includes both, and running them twice at two versions is how this
repository once produced a green Go job beside a red lint job. super-linter
handles every other language, with `VALIDATE_GO` and `VALIDATE_GO_MODULES` off:
neither mode can lint a multi-package module.

`govulncheck.yml` runs weekly as well as on pull requests, because advisories
land independently of commits. When it fails on a standard library finding, the
fix is usually to bump the `go` directive in `go.mod` **and** the builder image
in the `Dockerfile` together.

`licensed` still exists, but tracks Go rather than npm. New dependencies need
the `.licenses/` cache regenerated through the Licensed workflow dispatch.

## Conventions

- Use `internal/actions` for log output, never `fmt.Print` or `log`.
- Document exported identifiers; avoid comments that restate the code.
- Update `README.md` when inputs, outputs, or usage change.
- Cover edge cases in tests, not just the happy path.
- Markdown files are hard wrapped at 80 columns: `prettier` reflows them and
  `markdownlint` flags what is over. Text written into GitHub is not: issue and
  pull request bodies, comments, and release notes are rendered with hard line
  breaks on, so every newline inside a paragraph becomes a `<br>` and wrapped
  prose arrives as ragged short lines that ignore the reader's width. Paragraphs
  there go on one line each, however long.
- Actions are pinned by SHA, and the repository restricts which actions may run
  at all. A new third-party action needs adding to the allowlist under Settings
  → Actions before it will resolve; an unlisted one fails the whole workflow at
  startup with no annotation.
