# Feature: Godoc Coverage on Exported Identifiers

## Context

Pebblify currently ships with 70 undocumented exported identifiers spread across
9 internal packages (`batcher`, `completion`, `fsutil`, `health` (partial),
`metrics`, `migration`, `progress`, `prom`, `state`, `verify`) and 12 packages
missing a package-level doc comment (`cmd/pebblify`, `internal/batcher`,
`completion`, `fsutil`, `health`, `metrics`, `migration`, `progress`, `prom`,
`state`, `verify`, `daemon/api`, `daemon/notify`).

Go tooling (`go doc`, `golint`/`revive` `exported` rule, `pkg.go.dev`) relies on
the convention that every exported identifier carries a doc comment whose first
word is the identifier itself and every package has a single `// Package <name>
...` sentence. Without it, the public API surface is opaque to downstream
integrators and future maintainers.

Reference: [Go Doc Comments](https://go.dev/doc/comment).

Roadmap entry: v0.4.1 — first item under "In progress / next patch".

## Requirements

1. [confirmed] Every exported (PascalCase) identifier in `cmd/**/*.go` and
   `internal/**/*.go` (excluding `*_test.go`) carries a doc comment.
2. [confirmed] Every package under `cmd/` and `internal/` has a single file
   containing a `// Package <name> ...` sentence.
3. [confirmed] Doc-comment format: `// Identifier <description>` placed directly
   above the declaration with no intervening blank line.
4. [confirmed] Scope is limited to exported symbols. Unexported identifiers are
   out of scope.
5. [confirmed] No behavior change. Comment-only diff.

## Architecture

### Package placement

No new packages. Edits touch existing files in:
- `cmd/pebblify/`
- `internal/batcher/`, `completion/`, `fsutil/`, `health/`, `metrics/`,
  `migration/`, `progress/`, `prom/`, `state/`, `verify/`
- `internal/daemon/api/`, `internal/daemon/notify/`

### Package doc convention

Each package gets a single `doc.go` file (or the existing canonical file) with:

```
// Package <name> <one-line purpose>.
//
// <optional extended description>
package <name>
```

### Identifier doc convention

```
// <Identifier> <verb phrase describing what it is or does>.
func <Identifier>(...) ...
```

### Configuration

None. No flags, no env vars, no config keys.

### Error types

None added.

### Dependencies

None added. Verification uses toolchain already tracked by `@lead-dev`:
`revive` (already in `golangci-lint` config) and `go doc` (stdlib).

## Interface contract

No interface changes. This is a documentation-only change.

## Package interaction diagram

Not applicable — no code flow changes.

## Scope

Include:
- `cmd/**/*.go`
- `internal/**/*.go`

Exclude:
- `*_test.go` (tests do not require `exported` rule)
- Generated files (none currently)
- Vendored code (none currently)

## Non-goals

- Documenting unexported (lowercase) identifiers.
- Writing `Example_` test functions.
- Generating a godoc/pkg.go.dev site or static HTML.
- Changing code behavior, signatures, or visibility.
- Rewriting existing correct doc comments for style.

## Acceptance criteria

1. `golangci-lint run ./...` passes with the `revive` `exported` rule enabled
   and zero warnings from it.
2. `go doc <pkg>.<Identifier>` returns a non-empty description for every
   exported symbol under `cmd/` and `internal/`.
3. `go doc <pkg>` returns a non-empty package synopsis for every package under
   `cmd/` and `internal/`.
4. Existing test suite (`go test ./...`) passes unchanged — comments do not
   alter behavior.
5. Optional spot check: `go run golang.org/x/tools/cmd/godoc -http=:6060` serves
   a populated index for `github.com/Dockermint/pebblify/...`.

## Verification commands

```
# Lint gate (exported rule enabled in .golangci.yml)
golangci-lint run ./...

# Per-package synopsis check
go doc ./cmd/pebblify
go doc ./internal/batcher
go doc ./internal/completion
go doc ./internal/fsutil
go doc ./internal/health
go doc ./internal/metrics
go doc ./internal/migration
go doc ./internal/progress
go doc ./internal/prom
go doc ./internal/state
go doc ./internal/verify
go doc ./internal/daemon/api
go doc ./internal/daemon/notify

# Regression guard
go build ./cmd/... ./internal/...
go vet ./...
go test ./...

# Optional local godoc server
go run golang.org/x/tools/cmd/godoc -http=:6060
```

## Owning agents

- `@go-developer` — implementation (add doc comments, add `doc.go` files).
- `@qa` — re-runs `go test ./...` to confirm no regression. No new tests
  required: adding comments does not change behavior.
- `@lead-dev` — confirm `revive` `exported` rule active in `.golangci.yml`.
- `@reviewer` — verify every exported identifier and package has a doc comment
  matching Go convention.

## Risks

None. Pure documentation change. No runtime, build, or API impact.

## Testing strategy

No new unit tests. Existing `go test ./...` run by `@qa` is sufficient as a
regression guard.

## Open questions

None for CEO. Scope and convention fully specified by CEO brief.

## Roadmap entry

Added to `docs/ROADMAP.md` under v0.4.1 — "In progress / next patch".
