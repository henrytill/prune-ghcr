# Go port plan

Working document for the `go-port` branch, which replaces the TypeScript action
with a Go binary shipped as a prebuilt Docker action.

## Why

The npm dependency surface is the maintenance cost here: Dependabot churn, a
`.licenses/` cache to regenerate, an `overrides` check to keep honest, and a
committed `dist/` guarded by its own workflow. The action itself is ~440 lines
that make a handful of HTTP calls.

A Go rewrite removes most of that: no `dist/` to commit or guard, no `overrides`
check, and a dependency surface of two well-maintained libraries rather than a
lock file. The cost is that Actions has no `runs.using:` that invokes a compiled
binary — only `node24`, `docker`, and `composite` — so the action ships as a
Docker action referencing a prebuilt image.

## Target shape

```text
cmd/prune-ghcr/main.go        <- src/index.ts + src/main.ts
internal/actions/actions.go   <- the @actions/core surface we actually use
internal/api/api.go           <- src/api.ts
internal/registry/registry.go <- src/registry.ts
internal/prune/prune.go       <- src/prune.ts
internal/retry/retry.go       <- src/retry.ts
Dockerfile                    <- golang builder -> distroless/static
```

`distroless/static` rather than `scratch`: we need its CA certificates for TLS
to ghcr.io and the API.

`api` and `registry` are thin wrappers over their libraries. What they still own
is the error vocabulary — converting a failure into one that says whether
retrying could help — and the conversion from the libraries' types into ones
`prune` can consume without importing either.

## Dependencies

`google/go-github` covers `api.go` and `google/go-containerregistry` covers
`registry.go`. An earlier draft of this document argued for the standard library
alone; that was reversed deliberately, and the reversal is worth recording
honestly, because the original objection was correct on the facts.

What we get:

- `remote.Get` negotiates manifest media types itself, so the hand-maintained
  `Accept` list is gone, and it **verifies that content hashes to the digest
  requested** — the hand-rolled client did not.
- `go-github` parses `updated_at` and follows pagination, and its
  `WithEnterpriseURLs` already knows that `api.github.com` must not have
  `/api/v3/` appended while a GHES URL must.
- The user-versus-organization split becomes a choice of method rather than a
  hand-built path string, which is harder to get subtly wrong.

What it costs, measured rather than assumed:

- 2 direct requirements, 10 indirect, 50 modules in the full graph. `docker/cli`
  and `sirupsen/logrus` are in the tree, via `authn`, exactly as the earlier
  draft predicted.
- The binary grows from 6.2 MB to 7.0 MB on amd64.
- `.licenses/` does not go away, it changes ecosystem: `.licensed.yml` gains
  `go: true` and the cache needs regenerating through the Licensed dispatch.
- `govulncheck` now has a real dependency surface to scan, which cuts both ways.
- `go-github` cuts a major roughly monthly and the major is in the import path,
  so each bump touches source. Dependabot will not group those.

## Order of work

1. `internal/actions` — `Input`, `BoolInput`, `SetSecret`, `SetOutput`, `Info`,
   `Warning`, `Error`. Commands go to stdout. `SetOutput` appends to
   `$GITHUB_OUTPUT` in the heredoc form rather than as `name=value`, so a value
   containing a newline cannot forge a second output. There is no `SetFailed`:
   `run` returns an error and `main` turns it into one `::error::` and a
   non-zero exit, which is what `core.setFailed` amounts to.

   Only the property-less command form is implemented, which sidesteps the
   second escaping table — message data needs `%25`, `%0D`, `%0A`, while command
   _property_ values additionally need `%3A` and `%2C`. Adding a command with
   properties means adding that table.

1. `internal/retry` — sets the error vocabulary the rest of the port uses.
   `NonRetryableError` becomes a typed error matched with `errors.As`;
   `statusError(msg, status)` ports directly. Make the base delay injectable so
   tests do not sleep. It goes second, not first: `src/retry.ts:1` imports
   `@actions/core` for the per-attempt `core.warning`. Take that as a
   `warn func(string)` parameter rather than a package dependency — the retry
   tests can then assert on the log output without a global.
1. `internal/api` — the concrete client, with the same three methods.
   `go-github` owns pagination and JSON decoding, so what remains is selecting
   the user or organization method, converting `PackageVersion` into a
   `ContainerVersion` prune can use, and decoding the container tags out of
   `Metadata`, which is a `json.RawMessage` because the same field is an array
   on webhook payloads. The interface itself does not live here: Go declares
   interfaces at the consumer, so it moves to `internal/prune` along with
   `ManifestReader`.
1. `internal/registry` — `remote.Get` handles the token exchange, the media type
   negotiation, and digest verification, so `MANIFEST_ACCEPT` does not carry
   over. The lowercase-repository rule keeps its comment. Auth is lazy now, so a
   bad token surfaces at the first manifest read rather than at construction.
1. `internal/prune` — mechanical, since it already takes its collaborators as
   interfaces. It declares `VersionsAPI` and `ManifestReader`, so the test fakes
   live next to the interfaces they satisfy. It also takes a `Logger`, for the
   same reason `withRetry` takes a `warn`.
1. `cmd/prune-ghcr` — input reading and validation, then the failure handling
   described under `internal/actions` above.
1. `Dockerfile` and the image publish workflow.
1. Bootstrap the image before `action.yml` can reference it. Publish from this
   branch under a prerelease tag, point `action.yml` at that digest, and run
   `dry-run.yml` against a real package. This is where the container plumbing
   gets proven, so check it deliberately rather than just reading the exit code:
   inputs arrive as `INPUT_*`, `$GITHUB_OUTPUT` is writable at the path Actions
   mounts in, `::add-mask::` on stdout still masks, and a non-zero exit still
   fails the step. Run it on `ubuntu-24.04-arm` too, since we publish
   multi-arch.
1. Delete the npm side, in one commit, once `dry-run` is green.
1. Cut the real release: publish from the final commit, repoint `action.yml` at
   that digest, tag.

## Invariants to preserve

These are the reasons the action is correct; carry them across unchanged.

- A tagged manifest that cannot be read aborts the whole run. Wrap with
  `fmt.Errorf("could not read %s: %w", ...)`.
- Individual delete failures are counted and the run continues, then the action
  fails at the end.
- The token has all whitespace stripped, and an empty token is a hard failure
  rather than a no-op.

## Porting traps found while reading the source

- `src/main.ts:18` uses `/\s/g`, which strips **all** whitespace, not just the
  ends. That is `strings.Map` dropping `unicode.IsSpace`, not `TrimSpace`.
- `src/prune.ts:73` iterates `[...keep].sort()`. In TypeScript the sort is
  arguably cosmetic; in Go, map iteration order is randomized, so
  `slices.Sorted` is load-bearing for reproducible log output.
- `updated_at` parsing is stricter than `Date.parse`. `go-github` decodes it
  into a `Timestamp`, so the failure mode moves: rather than one bad value, a
  malformed timestamp fails the whole list call. An **absent** one leaves the
  zero time, which is the dangerous case.
- The direction of that failure matters, and the original is dangerous.
  `src/prune.ts:88` compares `Date.parse(...) > cutoff`; an unparseable
  `updated_at` yields `NaN`, the comparison is false, and the version gets
  **deleted**. The zero time would do the same, being older than any cutoff, so
  `prune` skips a version whose timestamp is absent, on the same "refuse to
  guess" principle as the unreadable manifest above.
- `http.Client{}` has no default timeout, where `@actions/http-client` sets a
  socket timeout. Without an explicit one, a wedged manifest read stops being a
  retryable failure and becomes a job that hangs to the six-hour limit.
  `go-github` takes `WithTimeout`; go-containerregistry's is bounded by the
  context.
- JS number parsing is looser than `strconv`. `src/main.ts:31` uses `Number()`,
  where `Number(' 12 ')` is `12` and `Number('')` is `0`; `ParseFloat` errors on
  both. The `'0'` default hides this until a caller passes
  `min-age-hours: ${{ inputs.age }}` with `age` unset, which is green today and
  a hard failure after the port.
- `core.getBooleanInput` accepts exactly `true|True|TRUE|false|False|FALSE` and
  throws otherwise. `strconv.ParseBool` is close but not identical — it also
  takes `1`, `0`, `t`, `f` — so a `dry-run: 1` that fails today would silently
  start working. Match the Actions set explicitly.
- `src/registry.ts` hardcodes `ghcr.io`, so it needs an injectable host before
  `httptest` can reach it. `src/api.ts` already takes a base URL.
- A test registry must answer the `/v2/` ping and the token endpoint before any
  manifest read, and must serve content that actually hashes to the digest
  requested, because `remote.Get` verifies it.

## Tests

`httptest.NewServer` replaces the entire ESM mocking apparatus: the
`jest.unstable_mockModule` ordering, the "mock `HttpClient` as a plain class"
trap, and the retryable-request-sleeps-through-the-timeout trap all disappear.

`__tests__/prune.test.ts` ports nearly line for line against fake
implementations of the two interfaces.

## Packaging

`action.yml` becomes:

```yaml
runs:
  using: docker
  image: docker://ghcr.io/henrytill/prune-ghcr@sha256:...
```

Not `image: Dockerfile` — that rebuilds on the runner every job, which is the
slow path this design exists to avoid.

While the port is on the branch, though, `action.yml` does say
`image: Dockerfile`, because there is no image to reference yet. That is what
dissolves the bootstrap problem: the action stays runnable from a checkout, so
`ci.yml`'s existing empty-token job builds the container and proves the plumbing
before anything is published. The digest replaces it at the bootstrap step
below.

Docker over `composite`, which is the only other way to run a compiled binary. A
composite action would lift the Linux-only constraint below and drop the
registry dependency, but it cannot invoke the binary without glue in another
language: shell to locate-or-fetch and exec it, plus an `env:` block mapping
every input to `INPUT_*` by hand, since composite steps — unlike Docker and JS
actions — do not get those set for them. That is a second implementation of the
input contract, in YAML, kept in sync by discipline. Docker is the only option
where `action.yml` hands the process its environment and nothing sits between
the runner and `main.go`. The constraints below are the price and they are
acceptable.

By digest, not `:v1`. A floating tag would be simpler to release, but it breaks
a property the action has today: `uses: henrytill/prune-ghcr@<sha>` currently
pins the exact code that runs, and against a mutable tag it would pin only an
`action.yml` that points somewhere else. A consumer following the usual
supply-chain advice would silently pick up whatever was pushed to `:v1` last.
The digest makes the pin real and forces the release order rather than leaving
it to be remembered: build and push, capture the digest, commit `action.yml`,
then tag. `script/release` grows a step.

Consequences to handle:

- Multi-arch (amd64 and arm64), so ARM runners work.
- The image must be public: a Docker action cannot authenticate a private pull.
- The action's ability to start now depends on ghcr.io, the same registry it
  prunes. A bad ghcr.io day currently produces a clear failure from inside the
  action; after the port it produces a runner-level pull failure before our code
  runs. Acceptable, but it is the real cost of the packaging change.
- Linux runners only — and self-hosted macOS and Windows runners are out too,
  permanently, for anyone consuming this. README line added.
- `dry-run.yml` uses `./`, so it cannot go green until an image exists that was
  built from the commit being tagged. See the bootstrap in the order of work.

## Files removed in the final commit

`src/`, `__tests__/`, `__fixtures__/`, `dist/`, `package.json`,
`package-lock.json`, `tsconfig.json`, `rollup.config.ts`, `jest.config.js`,
`eslint.config.mjs`, `.prettierrc.yml`, `.prettierignore`, `.licenses/`,
`.licensed.yml`, `.node-version`, `script/local-action`,
`script/check-overrides`, and the `check-dist`, `check-overrides`, and
`licensed` workflows.

## Open items

- `.devcontainer` needs a Go feature so the toolchain is not just whatever the
  host happens to have.
- Coverage badge: `go test -coverprofile` plus a badge step, or drop the badge.
  The Go CI job already writes `coverage.out` and nothing consumes it.
- `staticcheck` and `govulncheck` run via `go run tool@latest`, which keeps them
  out of `go.mod` at the cost of not being pinned. Revisit if a release of
  either breaks CI unprompted.
- CodeQL supports Go; only the language matrix changes.
- `package.json` held the release version of record. Something else has to: a
  `VERSION` file, or the git tag alone. Note that `action.yml` now carries the
  image digest, so it is already a file `script/release` must write — folding
  the version into the same step is cheaper than a separate `VERSION`.
- `CLAUDE.md` needs a rewrite once the port lands. Most of it describes npm
  commands, the committed `dist/`, and the ESM mocking pattern.
