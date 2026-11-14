---
layout: default
title: Source of truth
---

# Source of Truth

Every concept is defined in **one** place; everywhere else it is only referenced.
Dual definitions are the number one cause of silent drift.

## Canonical sources

| Scope | Canonical file | Authority |
|---|---|---|
| Architecture, layers, algorithms, task order | `docs/ARCHITECTURE.md` | PRIMARY |
| Domain types, constants, naming | `internal/domain/*.go` | PRIMARY |

## Concept → canonical home

| Concept | Canonical | Duplication ban |
|---|---|---|
| `TestID`, `RunSpec`, `RunResult` | `internal/domain/{test,run}.go` | No similar type defined in another package |
| `Kind` (repeat/order/parallel/env) | `internal/domain/witness.go` | Probes do not define their own constant |
| Generated file/function names | `internal/domain/names.go` | No file-name string literals anywhere else |
| Sentinel errors + exit codes | `internal/domain/errors.go` | The CLI does not keep its own code table |
| Capsule schema | `internal/domain/capsule.go` (`SchemaVersion`) | JSON field names derive from here |
| Budget defaults | `internal/policy/budget.go` | — |
| `go test` flag mapping | `internal/gotest/runner.go` | No other package calls `exec.Command` |

## Invariants

1. **`domain` imports no internal package.** A reverse dependency is a build error.
2. **`go test` is only invoked inside `internal/gotest`.** An
   `exec.Command("go", ...)` anywhere else is a bug.
3. **A breaking capsule schema bumps `SchemaVersion`**; old capsules stay readable.
4. **Exit codes are immutable.** CI scripts bind to them; codes may be added,
   never reshuffled. Exit 7 covers package failure and incomplete execution.
   See the [exit code table](ARCHITECTURE.md#101-typed-errors--exit-codes).
5. **Generated test files are never edited by hand** (they carry a "DO NOT EDIT" header).
