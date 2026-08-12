# Security

## Reporting a vulnerability

Report suspected vulnerabilities through
[GitHub's private vulnerability reporting](https://github.com/henrytill/prune-ghcr/security/advisories/new)
rather than a public issue.

## What this action does with your token

The action needs a token that can delete package versions, which is a
destructive permission. It:

- strips whitespace from the token and calls `core.setSecret` on the result, so
  the value is masked in logs even if the secret was pasted with a trailing
  newline
- sends the token only to `api.github.com` (or `GITHUB_API_URL`) and to
  `ghcr.io`, and never writes it to a file or passes it to a subprocess
- runs no subprocesses at all: there is no `gh` CLI, no shell, and no dynamic
  code loading

## Recommendations for consumers

**Pin by commit SHA**, not by tag. Tags can be moved; a commit SHA cannot:

```yaml
uses: henrytill/prune-ghcr@1316af878763a5b5c1823fa9435b1def6ab09c4c # v1.0.0
```

**Use a fine-grained PAT** scoped to only the packages you want pruned, with
read and write on Packages. A classic PAT with `delete:packages` applies to
every package you own, so a leak is far more damaging. `GITHUB_TOKEN` cannot
delete versions of a user-owned package, which is why a PAT is required at all.

**Start with `dry-run: true`** when pointing the action at a new package, and
read the list it reports before letting it delete anything.

## How this repository is protected

- `main` requires a pull request, and the CI, `check-dist`, lint, and CodeQL
  checks must pass before merging
- `dist/` is rebuilt and diffed by the `check-dist` workflow on every pull
  request, so the committed bundle cannot drift from the source it claims to
  come from
- every third-party action is pinned by commit SHA, and the repository is
  restricted to an allowlist of actions
- `npm ci --ignore-scripts` in CI, so a compromised dependency's install script
  does not execute
- CodeQL, Trivy, `licensed`, gitleaks, and zizmor run over the codebase and its
  workflows
- workflows request read-only `GITHUB_TOKEN` permissions except where a write is
  structurally required, and no workflow uses `pull_request_target`
