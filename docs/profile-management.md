# Context profile management

REST endpoints share the existing HTTP server (`RE_HTTP_ADDR`, default `:8080`).
No database migration or extra deployment port is required.

| Method | Path | Result |
| --- | --- | --- |
| POST | /api/v1/context-profiles | Create, 201 with Location |
| GET | /api/v1/context-profiles | List, 200 |
| GET | /api/v1/context-profiles/{id} | Read, 200 |
| PATCH | /api/v1/context-profiles/{id} | Update, 200 |
| DELETE | /api/v1/context-profiles/{id} | Permanently delete, 204 |

Create requires a nonblank name and a nonempty valid selector. Providers default
to an empty object, description to an empty string, and enabled to true.
Responses include id, name, description, selector, providers, enabled, created_at,
and updated_at. List accepts enabled=true/false, limit (1–100, default 20), offset
(nonnegative, default 0), and returns items, total, limit, offset. Ordering is by
name then id. Errors use the same JSON envelope as rule management: 400 invalid,
404 not found, 409 duplicate name/concurrent change, 500 internal error.

## PATCH replaces objects

Omitted fields are preserved. Supplied selector or providers replace that entire
object, including arrays and nested additional_information. There is no deep
merge. Null top-level fields and unknown fields (including inside selector and
providers) are rejected. At least one field is required.

For example, replacing providers with {"metric":["cpu_usage"]} removes any previous
vdu, configuration, and link settings. Send the complete providers object if
those settings should remain.

- `{"providers":{}}` clears declared providers.
- `{"selector":{}}` is invalid because a selector needs at least one condition.
- `{"enabled":false}` disables the profile without deleting it.

Validation reuses contextbuilder validation and makes no provider calls. Profile
JSON is limited to 1 MiB after encoding; HTTP bodies to 8 MiB. NUL is rejected
because PostgreSQL JSONB does not support it.

## Examples

```sh
curl -X POST http://localhost:8080/api/v1/context-profiles \
  -H 'Content-Type: application/json' \
  -d '{"name":"cpu_overload","selector":{"alert_types":["cpu"]},"providers":{"metric":["cpu_usage"]}}'

curl 'http://localhost:8080/api/v1/context-profiles?enabled=true&limit=20'

curl -X PATCH http://localhost:8080/api/v1/context-profiles/UUID \
  -H 'Content-Type: application/json' \
  -d '{"providers":{},"enabled":false}'
```

Committed changes apply on the next context builder profile load. In-flight
builds may use their previous definitions. Clearing providers only clears the
profile declarations; automatic context enrichment remains in effect.
Rules are independent: removing a profile can leave rules without needed context.

Updates compare the stored updated_at against the timestamp read during the
request. A conflict returns 409. This does not detect a stale copy held by a
client before the request; there is no ETag contract.

## Validation

Run `go test -race ./...`. Set TEST_DATABASE_URL to a disposable PostgreSQL
database for integration tests. Tests create and drop an isolated schema and
verify that the context builder repository observes profile changes.
