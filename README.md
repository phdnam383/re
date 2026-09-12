# re — Incident Analysis Engine

A gRPC service that answers one question: given an alert, what caused it and
what should be done about it.

```text
AnalyzeAlertByRule (gRPC)
    → Context Builder     gathers profile-selected and automatic alert-derived context
    → RCA Rule Engine     runs the operator's GRL rules over that context
    → AlertRuleAnalysisResponse
```

Both stages are primarily driven by data in PostgreSQL — `context_profile` says
what may be fetched and `rca_rule` says what may be concluded. The Context
Builder can also add narrowly scoped provider work inferred from alert fields;
currently `additional_information.remote_ip` adds a Link Provider ping.

Each enabled `rca_rule` document executes exactly once over the complete
context snapshot. Collection facts such as `Ctx.Vnfc.DownPathsInVDU(vdu)` perform
entity iteration in Go; GRL rules do not run once per VDU/VNFC and do not name
scaling-created instance paths.

## Requirements

- Go 1.25.7
- PostgreSQL with the `ltree` and `pgcrypto` extensions
- `buf` (only to regenerate protobuf; the generated code is committed)

## Configuration

The engine reads its environment once at startup.

| Variable | Required | Default | Meaning |
|---|:---:|---:|---|
| `DATABASE_URL` | yes | — | PostgreSQL holding topology, context profiles and RCA rules |
| `RE_GRPC_ADDR` | yes | — | Listen address, e.g. `:30051` |
| `RE_RCA_RULE_TIMEOUT` | no | `800ms` | Timeout for one `rca_rule` row |
| `RE_CONFIGURATION_TIMEOUT` | no | `2s` | Timeout for one Configuration Provider `GET` |
| `RE_VDU_TIMEOUT` | no | `2s` | Timeout for one VDU Provider `GET` |
| `RE_PROBE_TIMEOUT` | no | `2s` | Timeout for one ping attempt, sent to the Link Provider probe API |
| `RE_METRIC_TIMEOUT` | no | `2s` | Timeout for one Metric Provider `GET` |
| `RE_CONFIGURATION_BASE_URL` | no | `http://config` | Base URL for the Configuration Provider |
| `RE_VDU_BASE_URL` | no | `http://api/v1/vdus` | Base URL for the VDU Provider |
| `RE_PROBE_BASE_URL` | no | `http://api/v1/probe/ping` | Base URL for the Link Provider probe endpoint |
| `RE_METRIC_BASE_URL` | no | `http://metrics` | Base URL for the Metric Provider |

Durations use Go syntax (`800ms`, `2s`, `1m30s`). An unset duration takes the
default; a value that does not parse, or is zero or negative, **fails
startup** — an engine running a different timeout than the one configured
would be debugged by reading the wrong number.

For a Link Provider request, the HTTP deadline is separate from the ping
timeout. It is calculated as `3 * RE_PROBE_TIMEOUT + 500ms`, allowing the three
configured ping attempts to finish plus transport and response-decoding
overhead.

There is no `RE_DB_DRIVER` (the driver is always `pgx`). The base URL variables
above are each provider's endpoint host/path; the full request URL is built at
runtime from the base URL plus the collected target path and key. An unset base
URL falls back to the package default shown, so they need not be set unless the
environment points at a different NF API.

Configuration profiles declare key lists, for example
`"configuration": ["log_file_count", "log_level"]`. Each key is associated
with the matching alert's full `source_path`. When constructing the request,
the provider takes its first two labels: `ims.vdu_sb_logic.vnfc_sb_logic_1`
becomes `GET <base>/ims.vdu_sb_logic.log_file_count`. Existing database
profiles using `{path, key}` objects must be migrated to key strings when
deploying this version.

Context profile selectors optionally accept `"source_paths": ["ims.vdu_sb_logic"]`.
Each entry must be a VDU path with exactly two labels (`<namespace>.<vdu>`);
VNFC paths and wildcards are rejected. Matching checks the alert's top-level
`source_path`, accepting the VDU itself and its descendants, case-insensitively.
Multiple source paths are ORed, while source paths and the other selector fields
must all match the same alert. Omitting `source_paths` or using an empty list
leaves the source unrestricted. A selector containing only nonempty `source_paths`
is valid.

## Database

Schema and seed are a separate deployment step. The engine never applies
migrations; it only checks that it can connect.

```bash
psql "$DATABASE_URL" -f db/schema.sql
psql "$DATABASE_URL" -f db/seed_test.sql   # demo topology, profiles and rules
```

`db/seed_test.sql` is test data for the three shipped scenarios. It stores an
unreachable configuration URL (`http://api/v1/...`) on purpose: effective
configuration is never in PostgreSQL, and a seed that could be satisfied from
the database would hide the mistake the Configuration Provider exists to
prevent.

## Running

```bash
export DATABASE_URL='postgres://user:pass@localhost:5432/re?sslmode=disable'
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
  "alert": {
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
  }
}' -proto proto/engine.proto localhost:30051 mdaf.v1.FaultAnalysisService/AnalyzeAlertByRule
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

Root causes are merged by `(category, role, summary)`. Each root cause contains
the affected `components`; a component may carry one specialised recommended
action such as `RESTART_VNFC` or `SET_CONFIG`.

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
| `InvalidArgument` | Missing `request_id` or alert, or an alert without `id`/`source_path`. The message names the field. | Caller. |
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

[`deploy/k8s/`](deploy/k8s/) is a **throwaway test stack** — just enough to
exercise the engine: a namespace, a PostgreSQL (schema + seed loaded on first
start), the engine, and a dev stub for the NF configuration API. There is no
Secret and no schema Job: DB credentials are inline env literals, and postgres
loads `db/schema.sql` + `db/seed_test.sql` itself via `initdb`. Four numbered
manifests, one per service, all plain `kubectl apply -f` (no kustomize); a
[`Makefile`](Makefile) wraps the apply calls in dependency order.

```bash
make help                       # list the deploy targets

make deploy-all                 # whole stack, in dependency order
# or step by step:
make deploy-db                  # PostgreSQL (+ the SQL ConfigMap it mounts)
make deploy-engine              # the engine
make deploy-config              # dev stub for the NF configuration API
```

The SQL ConfigMap (`deploy/k8s/10-schema-configmap.yaml`) is **generated** by
`make gen-sql` from `db/schema.sql` + `db/seed_test.sql` and is gitignored —
the schema postgres loads stays the same file the tests apply, so there is no
second copy to drift. `make deploy-db` runs the generator first. The namespace
comes from `-n` (default `ifm-rule`, set in `00-namespace.yaml`); override with
`NS=...`.

postgres mounts its data on a volatile `emptyDir`, so the DB is wiped (and
schema + seed re-applied from scratch) on every pod start. initdb only runs on
an *empty* data dir, so to reload after editing `db/*.sql` mid-session, delete
the pod to recreate the volume:

```bash
make reinit-db && make deploy-db
```

Send it something:

```bash
kubectl -n ifm-rule port-forward svc/rule-engine 50053:50053 &

grpcurl -plaintext -proto proto/engine.proto -d @ 127.0.0.1:50053 \
  mdaf.v1.FaultAnalysisService/AnalyzeAlertByRule < request.json
```

The responses for the three seeded scenarios are byte-for-byte the files in
[`testdata/engine/`](testdata/engine/).

### What is development-only

| File | Replace with |
|---|---|
| `20-postgres.yaml` | A managed PostgreSQL. This one is single-replica, no backups, and runs the schema through `initdb` on a volatile `emptyDir` — every pod restart wipes and reseeds the DB. The DB credentials are inline env literals, not a Secret: they suit a throwaway test stack, nothing more. |
| `40-dev-configuration-stub.yaml` | Nothing — the real NF configuration API. The stub answers a fixture, and an engine reading a fixture instead of the NF's own configuration is the failure the Configuration Provider exists to prevent. |

The stub's Service is named `api` because `db/seed_test.sql` points the TPS
profile at `http://api/v1/...`. In-cluster DNS resolves that URL to the stub,
so the seeded profile works unchanged and nothing rewrites the database.

### Notes that cost something to learn

- **`runAsUser: 65532` must match the base image, not just be non-zero.**
  The image runs as UID 65532 (`USER 65532:65532` in the Dockerfile) and the
  pod sets `runAsNonRoot: true`; the UID has to match what the image expects.
- **initdb runs once, on an empty data dir.** The data is on a volatile
  `emptyDir`, so on every pod start the DB is recreated and schema + seed
  re-applied. To reload after editing `db/*.sql` mid-session (when the data dir
  already exists), delete the pod so the volume is recreated: `make reinit-db`.
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
