# re — Incident Analysis Engine

A gRPC service that answers one question: given a set of alerts, what caused
the incident and what should be done about it.

```text
AnalyzeIncident (gRPC)
    → Context Builder     gathers only what the operator's profiles allow
    → RCA Rule Engine     runs the operator's GRL rules over that context
    → AnalyzeIncidentResponse
```

Both stages are driven by data in PostgreSQL — `context_profile` says what may
be fetched, `rca_rule` says what may be concluded — so changing the engine's
behaviour is a database change, not a deployment.

## Requirements

- Go 1.25.7
- PostgreSQL with the `ltree` and `pgcrypto` extensions
- `buf` (only to regenerate protobuf; the generated code is committed)

## Configuration

The engine reads its environment once at startup.

| Variable | Required | Default | Meaning |
|---|:---:|---:|---|
| `RE_DB_DSN` | yes | — | PostgreSQL holding topology, context profiles and RCA rules |
| `RE_GRPC_ADDR` | yes | — | Listen address, e.g. `:30051` |
| `RE_CONFIGURATION_TIMEOUT` | no | `2s` | Timeout for one Configuration Provider `GET` |
| `RE_RCA_RULE_TIMEOUT` | no | `800ms` | Timeout for one `rca_rule` row |

Durations use Go syntax (`800ms`, `2s`, `1m30s`). An unset duration takes the
default; a value that does not parse, or is zero or negative, **fails
startup** — an engine running a different timeout than the one configured
would be debugged by reading the wrong number.

There is no `RE_DB_DRIVER` (the driver is always `pgx`) and no base URL for the
Configuration Provider — each `context_profile` carries the full URL of every
value it wants read.

## Database

Schema and seed are a separate deployment step. The engine never applies
migrations; it only checks that it can connect.

```bash
psql "$RE_DB_DSN" -f db/schema.sql
psql "$RE_DB_DSN" -f db/seed_test.sql   # demo topology, profiles and rules
```

`db/seed_test.sql` is test data for the three shipped scenarios. It stores an
unreachable configuration URL (`http://api/v1/...`) on purpose: effective
configuration is never in PostgreSQL, and a seed that could be satisfied from
the database would hide the mistake the Configuration Provider exists to
prevent.

## Running

```bash
export RE_DB_DSN='postgres://user:pass@localhost:5432/re?sslmode=disable'
export RE_GRPC_ADDR=':30051'

go run ./cmd/engine
```

Logs are JSON on stdout. The process stops cleanly on `SIGINT` or `SIGTERM`:
it stops accepting new calls, gives in-flight analyses up to 10 seconds to
finish, then forces the rest.

## Calling it

```bash
grpcurl -plaintext -d '{
  "request_id": "req-sipgw-0001",
  "incident": "inc-sipgw-0001",
  "alerts": [{
    "id": "aaaaaaaa-1111-4111-8111-111111111111",
    "source_path": "ims.vdu_sb_sip_core.vnfc_sb_sip_core_1",
    "alert_type": "COMMUNICATIONS_ALERT",
    "probable_cause": "LINK_TO_PEER_SIPGW_DOWN",
    "perceived_severity": "CRITICAL",
    "state": "ACTIVE",
    "created_at": "2026-06-18T00:00:00Z",
    "additional_information": {
      "dst_path": "ims.vdu_cs_loadbalancer_icscf.vnfc_cs_loadbalancer_icscf_1"
    }
  }]
}' -proto proto/engine.proto localhost:30051 mdaf.v1.IncidentAnalysisEngine/AnalyzeIncident
```

Server reflection is not enabled, so `-proto proto/engine.proto` is required.

Complete responses for the three shipped scenarios are in
[`testdata/engine/`](testdata/engine/).

## Reading the response

`status` carries three values. `context` and `rca` say how each stage went;
`overall` is what to act on.

| Status | Meaning |
|---|---|
| `COMPLETE` | Every provider answered, every rule ran, and at least one root cause was found. |
| `NO_CONCLUSION` | Everything ran correctly and no rule matched. A statement about the rule set, not a failure — retrying will not change it. |
| `PARTIAL` | The answer was reached with less than the full picture: some context is missing, some rule row failed, or the run was cut short. Root causes may still be present. |

`overall` always equals `rca`, because the rule engine already lowers itself to
`PARTIAL` when it ran over an incomplete context — it is the stage that saw
both the evidence and the gaps.

`meta.missing_context` names every target that was asked for and not obtained,
with a `reason` (`NOT_FOUND`, `QUERY_FAILED`, `REQUEST_FAILED`, `HTTP_STATUS`,
`TIMEOUT`, `EMPTY_BODY`, `INVALID_JSON`). Provider and entity alone cannot
separate "the row does not exist" from "the backend was unreachable", and only
the second is an operator's problem. A rule that failed never appears here —
that is a problem with the rule set, and it is in the server log.

## Errors

`PARTIAL` and `NO_CONCLUSION` are successful responses, not errors.

| gRPC code | When | Fix |
|---|---|---|
| `InvalidArgument` | Missing `request_id`, `incident`, alerts, or an alert without `id`/`source_path`. The message names the field. | Caller. |
| `FailedPrecondition` `missing context_profile` | No enabled profile's selector matched any alert. | Add or enable a `context_profile`. |
| `FailedPrecondition` `missing rca_rule` | No enabled RCA rule. | Enable an `rca_rule`. |
| `Canceled` / `DeadlineExceeded` | The caller stopped waiting. | Caller. |
| `Internal` | Anything else. | Read the server log. |

`Internal` responses carry a deliberately generic message. The real error is
assembled from whatever broke — a DSN, a SQL fragment, a URL with credentials,
a piece of rule content — and none of that belongs on a wire the caller does
not own. It is logged server-side in full.

## Tests

```bash
go test ./...          # unit, bufconn and golden tests; no database needed
go test -race ./...    # needed: the provider fan-out and the compile cache are concurrent
go vet ./...
```

PostgreSQL-backed tests skip unless a DSN is set. **Point them at a throwaway
database** — each test creates its own schema, applies `db/schema.sql` and
`db/seed_test.sql` into it, and drops it afterwards, but the seed itself is
test data.

```bash
export RE_TEST_DB_DSN='postgres://user:pass@localhost:5432/re_test?sslmode=disable'

go test ./internal/contextbuilder/postgres/   # provider SQL
go test ./internal/ruleengine/postgres/       # rule repository SQL
go test ./cmd/engine/                         # full end-to-end
```

Confirm nothing skipped:

```bash
go test ./... -v 2>&1 | grep -c -- '--- SKIP'   # want 0 with a DSN set
```

### Golden files

| Directory | Holds |
|---|---|
| `testdata/context_builder/` | Snapshots the Context Builder produces |
| `testdata/rca/` | What the rule engine concludes from those snapshots |
| `testdata/engine/` | The gRPC responses the whole engine returns |

Regenerate with `RE_UPDATE_GOLDEN=1 go test ./...`. Every golden is paired with
explicit assertions in the same package, because a golden regenerated after a
regression still passes on its own.

## Kubernetes

[`deploy/k8s/`](deploy/k8s/) brings up the engine, a PostgreSQL for it to talk
to, a Job that applies the schema, and a stub for the NF configuration API.

```bash
docker build -t re-engine:dev .

kubectl kustomize deploy/k8s --load-restrictor LoadRestrictionsNone \
  | kubectl apply -f -

kubectl -n re rollout status deploy/re-engine
```

`kubectl apply -k` will not work here: the SQL ConfigMap is generated from
`db/`, which is outside the kustomization directory, and only `kubectl
kustomize` accepts `--load-restrictor`. Generating the ConfigMap from `db/`
rather than from a copy under `deploy/` is deliberate — the schema the Job
applies is the same file the tests apply, so there is no second copy to drift.

Send it something:

```bash
kubectl -n re port-forward svc/re-engine 31951:30051 &

grpcurl -plaintext -proto proto/engine.proto -d @ 127.0.0.1:31951 \
  mdaf.v1.IncidentAnalysisEngine/AnalyzeIncident < request.json
```

The responses for the three seeded scenarios are byte-for-byte the files in
[`testdata/engine/`](testdata/engine/).

### What is development-only

| File | Replace with |
|---|---|
| `secret.yaml` | Credentials from a real secret store. The committed password is a placeholder and is in the git history — do not edit it in place and consider it handled. |
| `postgres.yaml` | A managed PostgreSQL. This is one replica, no backups, no replication. |
| `dev-configuration-stub.yaml` | Nothing — the real NF configuration API. The stub answers a fixture, and an engine reading a fixture instead of the NF's own configuration is the failure the Configuration Provider exists to prevent. |

The stub's Service is named `api` because `db/seed_test.sql` points the TPS
profile at `http://api/v1/...`. In-cluster DNS resolves that URL to the stub,
so the seeded profile works unchanged and nothing rewrites the database.

### Notes that cost something to learn

- **`runAsUser: 65532` is required, not decoration.** The image declares
  `USER nonroot` by name, and a kubelet with `runAsNonRoot: true` cannot
  resolve a name to a UID without running the container — so it refuses to
  start it. The number must match the base image.
- **Re-applying after a schema edit fails.** The generated ConfigMap name
  carries a content hash, but the Job's name does not, and a Job spec is
  immutable. Run `kubectl -n re delete job re-schema` first. That is left
  explicit so a re-apply cannot silently re-run `db/seed_test.sql` over
  whatever an operator changed.
- **No health API, so the probes are TCP.** It is still a real signal: the
  engine pings PostgreSQL before it binds the port, so a pod accepting
  connections has already proved it can reach its data.
- **`terminationGracePeriodSeconds` must exceed the shutdown grace period.**
  The engine gives in-flight analyses 10 seconds; a shorter pod grace period
  would have the kubelet SIGKILL it mid-shutdown.

## Protobuf

The generated code in `gen/mdafv1/` is committed and must never be hand-edited.

```bash
buf lint
buf generate
```

## Layout

```text
cmd/engine/                 composition root: config, wiring, shutdown
internal/analysis/          domain types, ports, validation, orchestration
internal/contextbuilder/    profile matching, plan resolution, providers
internal/ruleengine/        facts, GRL runtime, compile cache, runner
internal/transport/grpc/    protobuf mapping, error mapping, logging
gen/mdafv1/                 generated protobuf
proto/, db/, grule/, context_profile/, testdata/
```

`internal/analysis` owns the domain types and the ports; every other package
imports it and it imports none of them. Only `cmd/engine` knows which
implementation satisfies which port.
