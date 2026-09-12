# Rule management REST API

The engine serves REST on `RE_HTTP_ADDR` (default `:8080`) alongside the existing
gRPC analysis server. Both share the existing database. No schema migration is
required. Kubernetes exposes REST on the internal `rule-engine` Service at port
8080. API schema: [openapi.yaml](openapi.yaml).

```sh
curl -X POST http://localhost:8080/api/v1/rules \
  -H 'Content-Type: application/json' \
  -d '{"name":"example","rule_content":"rule Example { when true then Retract(\"Example\"); }","enabled":true}'

curl 'http://localhost:8080/api/v1/rules?enabled=true&limit=20&offset=0'
curl http://localhost:8080/api/v1/rules/UUID

curl -X PATCH http://localhost:8080/api/v1/rules/UUID \
  -H 'Content-Type: application/json' \
  -d '{"enabled":false,"salience":0}'

curl -X DELETE http://localhost:8080/api/v1/rules/UUID
```

Create defaults to `enabled=true`, `salience=0`, and an empty description. PATCH
preserves omitted fields, rejects null/unknown fields and requires at least one
field. Use an empty string to clear the description. Names are trimmed and must
be unique. GRL content is limited to 1 MiB; the JSON body is limited to 8 MiB.

Validation compiles GRL without running it or changing the execution cache.
Runtime context access and correctness still require execution tests. Context
profiles must separately provide any metrics/configuration required by a rule.

Each analysis loads enabled rules from PostgreSQL. Committed changes therefore
apply at the next rule load without restarting the process. Already loaded rules
may finish executing. Content changes invalidate the cached compilation by hash;
deleted rule cache entries currently remain allocated but are no longer selected.
Deleting or disabling the last enabled rule leaves the engine returning its
existing missing-rule error on analysis.

Updates compare the timestamp read by the service with the stored `updated_at`
to prevent overwrites during concurrent PATCH requests. This is not a client-side
revision/ETag contract and does not detect an edit based on a previously fetched
stale response. DELETE permanently removes the row; disabling retains it.

Run all tests with `go test ./...`. Set `TEST_DATABASE_URL` to a disposable
PostgreSQL database to include repository integration tests. Those tests create
and drop a unique schema and require schema-creation permission.
