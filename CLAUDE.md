# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`re` is a gRPC service (the "Incident Analysis Engine") that answers: given an
alert, what caused it and what should be done about it. It is a two-stage
pipeline:

```
AnalyzeAlertByRule (gRPC) → Context Builder → RCA Rule Engine → response
```

Behaviour is primarily data-driven: `context_profile` rows in PostgreSQL say
what context may be fetched, and `rca_rule` rows (GRL — the grule-rule-engine
DSL) say what may be concluded. Narrow automatic enrichers may also infer
provider work from alert fields; currently `remote_ip` triggers a Link probe.

## Commands

```bash
go run ./cmd/engine                      # run the server (needs DATABASE_URL, RE_GRPC_ADDR)
go build ./...                           # build everything
go test ./...                            # all unit + bufconn + golden tests (no DB)
go test -race ./...                      # race detector — the provider fan-out and compile cache are concurrent
go test -run TestName ./internal/...     # a single test
go vet ./...                             # vet

RE_UPDATE_GOLDEN=1 go test ./...         # regenerate golden files in testdata/
buf lint && buf generate                 # regenerate protobuf (generated code in gen/mdafv1/ is committed)
docker build -t re-engine:dev .          # build the image (Dockerfile at repo root)
```

PostgreSQL-backed tests skip unless `RE_TEST_DB_DSN` points at a throwaway
database; each test creates its own schema, applies `db/schema.sql` +
`db/seed_test.sql`, and drops it afterwards.

```bash
psql "$DATABASE_URL" -f db/schema.sql       # schema is a manual deploy step — the engine never migrates
psql "$DATABASE_URL" -f db/seed_test.sql    # three shipped scenarios
```

## Architecture

### Dependency rule — `internal/analysis` is the core

`internal/analysis/` owns the **domain types** (`ContextSnapshot`, `RCAResult`,
`RootCause`, `RuleDefinition`, …) and the **ports** (`ContextBuilder`,
`RCAAnalyzer` interfaces). Every other package imports it; it imports none of
them. Only `cmd/engine` (the composition root) wires concrete implementations
to ports. Keep this boundary — domain types stay in `analysis`, plumbing stays
outside it.

### The pipeline

1. **`internal/contextbuilder/`** — `Builder.Build` resolves a `Plan` from the
   alerts + matched profiles, applies automatic enrichers from `automatic.go`,
   then fans out to providers **concurrently**
   (`runProviders` spawns one goroutine per provider with a `sync.WaitGroup`).
   `builder.go` orchestrates; `plan.go` resolves; `selector.go` matches
   profiles to alerts; `snapshot.go` assembles the result. Provider
   implementations live in `postgres/` (VDU, Profile),
   `configuration/` (HTTP fetch against the NF configuration API), and
   `link/` (HTTP ping probe against the NF probe API).

2. **`internal/ruleengine/`** — `Engine.Analyze` loads enabled GRL rules,
   sorts by salience, and runs each over the snapshot via the grule runtime.
   The runtime behind GRL is a swappable `Runtime` ports so tests can drive
   rules without compiling. File roles:
   - `engine.go` — `Engine.Analyze`; loads rules, dispatches to `runRules`,
     derives the RCA status, logs rule failures.
   - `runner.go` — `runRules`/`runRule` per-rule loop, applies the rule
     timeout, owns the cause-set merge across rules.
   - `runtime.go` — the `Runtime`/`Session` ports and the
     `RuleExecution` builders (`completedExecution`, etc.).
   - `grule.go` — the production `GRLRuntime`/`grlSession`: binds `Ctx` (the
     facts) and `Result` (the sink) into the grule `DataContext` and runs
     `engine.NewGruleEngine()`.
   - `ports.go` — the `RuleRepository` port and `ErrRCARuleNotFound`.
   - `cache.go` — compiled-rule cache keyed by `rule.ID`; stores a SHA-256 of
     the content and recompiles only when the content changes.
   - `facts.go` — the `Facts` struct (the `Ctx` exposed to GRL): `Alert`,
     `Vdu`, `Vnfc`, `Link`, `Configuration` fact accessors.
   - `view.go` — the internal read-only index over the snapshot that backs the
     fact accessors.
   - `result.go` — the `Result` sink: `Assert`, `RecommendRestartVNFC`,
     `RecommendSetConfig`, plus the cause-merge logic.
   - `postgres/rule.go` — the `RuleRepository` implementation: `LoadEnabled`
     selects enabled `rca_rule` rows ordered by salience. (This is distinct
     from `internal/contextbuilder/postgres/`, which holds the VDU/Profile
     providers for context collection.)

3. **`internal/transport/grpc/`** — maps domain ↔ protobuf, handles gRPC error
   codes, logging interceptor. `mapper.go` is the boundary between
   `analysis.*` types and the generated `mdafv1.*` messages.

### Status semantics (non-obvious — read before touching status logic)

- There are **three** context-collection/RCA/overall statuses: `COMPLETE`,
  `NO_CONCLUSION` (everything ran, no rule matched — a statement about the rule
  set, not a failure), and `PARTIAL` (hit with less than the full picture:
  missing context, a failed rule row, or the run cut short). `FAILED`
  (`analysis.RCAStatusFailed`) is internal only — the `rca` status returned on
  the error path; it never reaches the wire because a failed analyze returns an
  error, not a result.
- `deriveStatus` (in `internal/ruleengine/engine.go`) computes the RCA status:
  if no rule completed → `FAILED` + error; else `PARTIAL` if the context was
  incomplete or any rule failed/skipped; else `COMPLETE` if a root cause was
  found, otherwise `NO_CONCLUSION`.
- The rule engine **lowers itself to `PARTIAL` when it ran over an incomplete
  context** — it is the stage that saw both the evidence and the gaps. With
  `NO_CONCLUSION` only reachable from a `COMPLETE` context, `overall` always
  equals `rca` (`deriveOverallStatus` even rejects a `PARTIAL` context paired
  with a non-`PARTIAL` rca as inconsistent).
- `meta.missing_context` reasons: `NOT_FOUND`, `QUERY_FAILED`,
  `REQUEST_FAILED`, `HTTP_STATUS`, `TIMEOUT`, `EMPTY_BODY`, `INVALID_JSON`.
  Provider + entity alone cannot separate "row absent" from "backend
  unreachable" — only the second is an operator problem. A rule that failed
  never appears here; that is logged server-side instead.

### Other things worth knowing

- **Generated protobuf is committed** under `gen/mdafv1/`. Never hand-edit it;
  regenerate with `buf generate` after editing `proto/engine.proto`.
- **Each enabled `rca_rule` executes exactly once** over the complete context
  snapshot. Collection facts like `Ctx.Vnfc.DownPathsInVDU(vdu)` do entity
  iteration in Go; GRL rules do not run once per VDU/VNFC and do not name
  scaling-created instance paths.
- The GRL DSL is documented in [`docs/grl-api.md`](docs/grl-api.md); example
  rules live in [`grule/`](grule/), profiles in
  [`context_profile/`](context_profile/), and the three end-to-end fixture
  responses in [`testdata/engine/`](testdata/engine/).
- Logs are JSON on `stdout`. `Internal` gRPC errors carry a deliberately
  generic message — the real error (DSN, SQL fragment, URL, rule content) is
  logged server-side in full and must not leak onto the wire.
- The engine pings PostgreSQL before binding the port, and shuts down by
  stopping new calls, giving in-flight analyses up to 10 seconds, then forcing
  the rest.
