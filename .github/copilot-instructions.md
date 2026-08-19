# Copilot Instructions

This GitHub Action is a Go binary shipped as a Docker action. `action.yml`
references a prebuilt image on `ghcr.io` **by digest**, so the action a consumer
pins by commit SHA is the code that actually runs. An image has to be published
before the commit that references it is tagged.

## Repository Structure

| Path                 | Description                                        |
| -------------------- | -------------------------------------------------- |
| `cmd/prune-ghcr/`    | Entry point: input handling and failure reporting  |
| `internal/actions/`  | The Actions toolkit surface the action uses        |
| `internal/api/`      | GitHub packages REST client, over `go-github`      |
| `internal/registry/` | Manifest reads, over `go-containerregistry`        |
| `internal/prune/`    | The pruning algorithm                              |
| `internal/retry/`    | Linear backoff and the retryable-error vocabulary  |
| `docs/`              | Working documents                                  |
| `.devcontainer/`     | Development Container Configuration                |
| `.github/`           | GitHub Configuration                               |
| `.licenses/`         | License Information                                |
| `.vscode/`           | Visual Studio Code Configuration                   |
| `.env.example`       | Environment variables for a local run              |
| `.golangci.yml`      | golangci-lint Configuration                        |
| `.licensed.yml`      | Licensed Configuration                             |
| `.markdown-lint.yml` | Markdown Linter Configuration                      |
| `.prettierrc.yml`    | Prettier Formatter Configuration                   |
| `.trivyignore`       | Trivy suppressions, mirroring the Dockerfile's own |
| `.yaml-lint.yml`     | YAML Linter Configuration                          |
| `action.yml`         | GitHub Action Metadata                             |
| `AGENTS.md`          | Instructions for coding agents                     |
| `Dockerfile`         | Builds the image the action runs                   |
| `go.mod`, `go.sum`   | Go Module Definition                               |
| `LICENSE`            | License File                                       |
| `README.md`          | Project Documentation                              |

## Testing

```bash
go test ./...
```

Tests live beside the code they test, as `_test.go` files. Collaborators are
reached through interfaces declared in the consuming package, so the tests use
plain fakes; `httptest` covers the HTTP clients. There is no mocking framework.

## Static Analysis

`golangci-lint` is the single owner, configured by `.golangci.yml`. It already
includes `govet` and `staticcheck`, so do not add those as separate steps.

```bash
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run ./...
```

## General Coding Guidelines

- Follow standard Go conventions; keep `gofmt` clean
- Changes should maintain consistency with existing patterns and style
- Document changes clearly and thoroughly, including updates to existing
  comments when appropriate
- Do not include basic, unnecessary comments that simply restate what the code
  is doing (focus on explaining _why_, not _what_)
- Wrap errors with `%w` and enough context to locate the failure
- Throw `NonRetryableError`, or build the error with `retry.StatusError`, for
  permanent failures: retrying a 403 or a 404 only burns the backoff
- Keep functions focused and manageable
- Use descriptive names that clearly convey their purpose
- Document exported identifiers with a comment beginning with the name
- Use `internal/actions` for logging rather than `fmt.Print` or `log`, so output
  reaches the workflow log as a workflow command
- After refactoring, run `go test ./...` and the linter above
- When writing tests, consider edge cases as well as the main path of success
- Avoid unnecessary complexity and always consider long-term maintainability

### Versioning

GitHub Actions are versioned by branch and tag name, following
[Semantic Versioning](https://semver.org/). There is no version field in a
manifest to update; the tag is the version of record. `script/release` moves the
tags, and it will ask whether the image has been published and `action.yml`
repointed at its digest first. It then creates the release as a draft, for the
one line it cannot write — see `.github/prompts/create-release-notes.prompt.md`.

## Pull Request Guidelines

When creating a pull request (PR), please ensure that:

- Keep changes focused and minimal (avoid large changes, or consider breaking
  them into separate, smaller PRs)
- Formatting, linting, and tests pass
- If necessary, the `README.md` file is updated to reflect any changes in
  functionality or usage

The body of the PR should include:

- A summary of the changes
- A special note of any changes to dependencies
- A link to any relevant issues or discussions
- Any additional context that may be helpful for reviewers

Each paragraph of the body goes on a single line, however long. GitHub renders
pull request and issue text with hard line breaks on, so wrapping a paragraph at
80 columns produces ragged short lines rather than prose that reflows. Markdown
files in the repository are wrapped as usual; this applies only to text typed
into GitHub.

## Code Review Guidelines

When performing a code review, please follow these guidelines:

- If there are changes that modify the functionality/usage of the action,
  validate that there are changes in the `README.md` file that document the new
  or modified functionality
- The invariants in `internal/prune` are the reasons the action is correct: a
  tagged manifest that cannot be read aborts the whole run, a version with no
  usable timestamp is skipped rather than deleted, and individual delete
  failures are counted before failing at the end. Treat changes to them as
  significant.
