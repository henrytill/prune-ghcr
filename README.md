# Prune untagged GHCR versions

[![CI](https://github.com/henrytill/prune-ghcr/actions/workflows/ci.yml/badge.svg)](https://github.com/henrytill/prune-ghcr/actions/workflows/ci.yml)
[![Check dist/](https://github.com/henrytill/prune-ghcr/actions/workflows/check-dist.yml/badge.svg)](https://github.com/henrytill/prune-ghcr/actions/workflows/check-dist.yml)
[![Coverage](./badges/coverage.svg)](./badges/coverage.svg)

Deletes untagged container versions of a GHCR package, keeping any that a tagged
multi-arch index still references.

Each build of a multi-arch image leaves the previous index and its per-platform
manifests untagged, so a package that is rebuilt on a schedule accumulates dead
versions. Deleting every untagged version is not a fix: the per-platform and
attestation manifests under a live image index carry no tags of their own, so
that deletes the contents of the image the tag points at. This action walks
every tagged manifest first and preserves the children it references.

## Usage

```yaml
prune:
  needs: build
  if: github.event_name != 'pull_request'
  runs-on: ubuntu-latest
  steps:
    - name: Prune untagged versions
      uses: henrytill/prune-ghcr@v1
      with:
        token: ${{ secrets.GHCR_PAT }}
```

Two overlapping publishing runs can race: the loser's prune may delete child
manifests the winner uploaded but has not tagged yet. Either serialize the
publishing workflow with a `concurrency` group, or set `min-age-hours` high
enough to cover a build.

This is a container action, so it runs on Linux runners only. `amd64` and
`arm64` are both published.

### Token

`GITHUB_TOKEN` cannot delete versions of a user-owned package, so this needs a
PAT:

- classic: `delete:packages` (plus `read:packages`)
- fine-grained: read and write on Packages

Whitespace is stripped from the token, because a PAT pasted into a secret with a
trailing newline produces an invalid `Authorization` header. An empty token
fails the run rather than silently skipping the prune.

### Inputs

| Input           | Default                               | Description                                       |
| --------------- | ------------------------------------- | ------------------------------------------------- |
| `token`         | _required_                            | PAT with permission to delete package versions.   |
| `owner`         | `${{ github.repository_owner }}`      | Owner of the package.                             |
| `package`       | `${{ github.event.repository.name }}` | Container package name.                           |
| `min-age-hours` | `0`                                   | Skip versions younger than this.                  |
| `dry-run`       | `false`                               | Report what would be deleted without deleting it. |

### Outputs

| Output    | Description                               |
| --------- | ----------------------------------------- |
| `deleted` | Number of versions deleted.               |
| `kept`    | Number of versions kept.                  |
| `failed`  | Number of versions that failed to delete. |

A failed delete does not stop the run — the remaining versions are still
attempted — but the run fails at the end. A tagged manifest that cannot be read
fails immediately and deletes nothing, since guessing there would break a live
tag.

## How it works

1. Lists every version of the package via the packages REST API
   (`/user/packages/...` when the token owns the package, `/orgs/...`
   otherwise), following pagination.
1. Exchanges the PAT for a `ghcr.io` pull token and fetches each tagged
   manifest, accepting the OCI and Docker index media types so a multi-arch
   image index comes back as an index rather than a single platform manifest.
1. Keeps every tagged version and every digest referenced by one, then deletes
   the untagged remainder that is older than `min-age-hours`.

Transient failures (network errors, 5xx, 429) are retried with a linear backoff;
permanent ones (403, 404) fail straight away.

## Development

```bash
npm ci
npm run test        # unit tests
npm run all         # format, lint, test, coverage, package
npm run bundle      # rebuild dist/ after changing src/
```

`dist/` is committed and checked by the `check-dist` workflow, so rebuild and
commit it whenever `src/` changes.

To run the action locally, copy `.env.example` to `.env`, fill in a token, and:

```bash
npm run local-action
```

That rebuilds the bundle and runs it under `node --env-file`, so a local run
exercises exactly what a workflow would.

## License

MIT
