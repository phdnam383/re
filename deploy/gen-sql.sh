#!/usr/bin/env bash
#
# Generate deploy/k8s/10-schema-configmap.yaml from db/schema.sql and
# db/seed_test.sql. The ConfigMap is produced here rather than committed so
# the schema postgres loads is always the same file the tests apply — there is
# no second copy under deploy/ to drift. See README "Kubernetes".
#
# Output is deterministic given the inputs (no timestamps), so running twice
# produces a byte-identical file: safe to regenerate in place.
#
# Replaces kustomize's configMapGenerator, which we dropped when moving to
# `kubectl apply -f`. Uses `kubectl create configmap --dry-run=client` so YAML
# escaping of the SQL body is handled correctly.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/.." && pwd)

schema_src="${RE_SCHEMA_SRC:-$repo_root/db/schema.sql}"
seed_src="${RE_SEED_SRC:-$repo_root/db/seed_test.sql}"
out="${RE_SQL_CONFIGMAP:-$repo_root/deploy/k8s/10-schema-configmap.yaml}"

if ! command -v kubectl >/dev/null 2>&1; then
  echo "gen-sql: kubectl is required (used in --dry-run=client mode only)" >&2
  exit 1
fi

for f in "$schema_src" "$seed_src"; do
  if [[ ! -f "$f" ]]; then
    echo "gen-sql: missing $f" >&2
    exit 1
  fi
done

# Keyed names match how postgres mounts them: /docker-entrypoint-initdb.d/
# schema.sql and /docker-entrypoint-initdb.d/seed_test.sql (20-postgres.yaml).
# initdb runs *.sql in alphabetical order, so schema.sql runs before
# seed_test.sql — the order CREATE statements need.
kubectl create configmap rule-engine-sql \
  --from-file=schema.sql="$schema_src" \
  --from-file=seed_test.sql="$seed_src" \
  --dry-run=client -o yaml \
  | sed '/^metadata:/a\  labels: {app.kubernetes.io/name: rule-engine-schema}' \
  > "$out"

echo "gen-sql: wrote $out"
