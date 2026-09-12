#!/bin/sh
#
# query.sh — chạy SQL trên PostgreSQL pod trong Kubernetes
#
#   sh query.sh "SELECT * FROM alert LIMIT 5"
#   sh query.sh -f migration.sql
#   sh query.sh -n "UPDATE alert SET state='CLEARED' WHERE id='...'"   # dry-run
#   cat big.sql | sh query.sh
#
set -eu

# ------------------------------------------------------------------ config ---
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
[ -f "$SCRIPT_DIR/.pgk8s.env" ] && . "$SCRIPT_DIR/.pgk8s.env"

# Defaults match the throwaway test stack under deploy/k8s/:
#   namespace ifm-rule, label app.kubernetes.io/name=postgres,
#   db "re", user "ifm", password inline env literal "1111" (no Secret).
# Override any of these via env or .pgk8s.env when pointing at a real cluster.
NAMESPACE="${NAMESPACE:-ifm-rule}"
POD_SELECTOR="${POD_SELECTOR:-app.kubernetes.io/name=postgres}"
POD_NAME="${POD_NAME:-}"
CONTAINER="${CONTAINER:-}"
DB_NAME="${DB_NAME:-re}"
DB_USER="${DB_USER:-ifm}"
DB_PASSWORD="${DB_PASSWORD:-1111}"
SECRET_NAME="${SECRET_NAME:-}"
SECRET_KEY="${SECRET_KEY:-postgres-password}"

FORMAT=table
DRY_RUN=0
ASSUME_YES=0
SQL_FILE=""

die() { printf 'query.sh: %s\n' "$*" >&2; exit 1; }

usage() {
  cat >&2 <<EOF
Cách dùng: sh query.sh [options] "SQL"
           sh query.sh -f file.sql
           cat file.sql | sh query.sh

Options:
  -f FILE     đọc SQL từ file
  -c          xuất CSV (chỉ dùng với SELECT)
  -j          xuất JSON (chỉ dùng với SELECT)
  -t          bỏ header và footer, chỉ in giá trị
  -n          dry-run: chạy trong BEGIN ... ROLLBACK, xem số dòng bị ảnh hưởng
  -y          không hỏi xác nhận với lệnh ghi (UPDATE/DELETE/DROP/...)
  -d DB       đổi database (mặc định: $DB_NAME)
  -h          trợ giúp

Cấu hình qua env hoặc file .pgk8s.env: NAMESPACE, POD_SELECTOR, POD_NAME,
CONTAINER, DB_NAME, DB_USER, DB_PASSWORD, SECRET_NAME, SECRET_KEY
EOF
  exit 1
}

# ----------------------------------------------------------------- options ---
while [ $# -gt 0 ]; do
  case "$1" in
    -f) SQL_FILE="${2:-}"; [ -n "$SQL_FILE" ] || die "-f cần tên file"; shift 2 ;;
    -c) FORMAT=csv;  shift ;;
    -j) FORMAT=json; shift ;;
    -t) FORMAT=tuples; shift ;;
    -n) DRY_RUN=1;   shift ;;
    -y) ASSUME_YES=1; shift ;;
    -d) DB_NAME="${2:-}"; [ -n "$DB_NAME" ] || die "-d cần tên database"; shift 2 ;;
    -h|--help) usage ;;
    --) shift; break ;;
    -*) die "option không hợp lệ: $1" ;;
    *)  break ;;
  esac
done

# ---------------------------------------------------------------- lấy SQL ----
if [ -n "$SQL_FILE" ]; then
  [ -f "$SQL_FILE" ] || die "không tìm thấy file: $SQL_FILE"
  SQL=$(cat "$SQL_FILE")
elif [ $# -gt 0 ]; then
  SQL="$*"
elif [ ! -t 0 ]; then
  SQL=$(cat)
else
  usage
fi

[ -n "$(printf '%s' "$SQL" | tr -d '[:space:]')" ] || die "SQL rỗng"

# ------------------------------------------------------------- tìm pod ------
command -v kubectl >/dev/null 2>&1 || die "không tìm thấy kubectl"

if [ -z "$POD_NAME" ]; then
  POD_NAME=$(kubectl -n "$NAMESPACE" get pod -l "$POD_SELECTOR" \
               --field-selector=status.phase=Running \
               -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  [ -n "$POD_NAME" ] || die "không có pod Running khớp '$POD_SELECTOR' trong ns '$NAMESPACE'"
fi

if [ -z "$DB_PASSWORD" ] && [ -n "$SECRET_NAME" ]; then
  DB_PASSWORD=$(kubectl -n "$NAMESPACE" get secret "$SECRET_NAME" \
                  -o jsonpath="{.data.$SECRET_KEY}" | base64 -d) \
    || die "không đọc được secret '$SECRET_NAME'"
fi

# psql luôn nhận SQL qua stdin, nên exec dùng -i (KHÔNG -t: TTY sẽ chèn \r)
psql_run() {
  if [ -n "$CONTAINER" ]; then
    kubectl -n "$NAMESPACE" exec -i "$POD_NAME" -c "$CONTAINER" -- \
      env PGPASSWORD="$DB_PASSWORD" psql -U "$DB_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 "$@"
  else
    kubectl -n "$NAMESPACE" exec -i "$POD_NAME" -- \
      env PGPASSWORD="$DB_PASSWORD" psql -U "$DB_USER" -d "$DB_NAME" \
      -v ON_ERROR_STOP=1 "$@"
  fi
}

# ------------------------------------------------- phát hiện lệnh ghi -------
# lấy từ khoá đầu tiên, bỏ qua comment và khoảng trắng
FIRST_WORD=$(printf '%s\n' "$SQL" \
  | sed -e 's/--.*$//' -e '/^[[:space:]]*$/d' \
  | head -n 1 | tr '[:lower:]' '[:upper:]' \
  | sed -e 's/^[[:space:]]*//' -e 's/[[:space:];(].*$//')

IS_WRITE=0
case "$FIRST_WORD" in
  UPDATE|DELETE|INSERT|DROP|TRUNCATE|ALTER|CREATE|GRANT|REVOKE|COPY) IS_WRITE=1 ;;
esac

if [ "$IS_WRITE" -eq 1 ] && [ "$DRY_RUN" -eq 0 ] && [ "$ASSUME_YES" -eq 0 ]; then
  if [ -t 0 ] || [ -t 1 ]; then
    printf 'Lệnh ghi (%s) trên db "%s" @ pod %s.\n' "$FIRST_WORD" "$DB_NAME" "$POD_NAME" >&2
    printf 'Gõ yes để chạy (hoặc dùng -n để dry-run, -y để bỏ hỏi): ' >&2
    read -r ANSWER < /dev/tty || ANSWER=""
    [ "$ANSWER" = "yes" ] || die "đã hủy"
  else
    die "lệnh ghi ($FIRST_WORD) trong chế độ non-interactive — thêm -y nếu chắc chắn"
  fi
fi

# ------------------------------------------------------------- build & run --
# dry-run: bọc trong transaction rồi rollback, vẫn thấy được số dòng ảnh hưởng
if [ "$DRY_RUN" -eq 1 ]; then
  PAYLOAD=$(printf 'BEGIN;\n%s\nROLLBACK;\n' "$SQL")
  printf 'query.sh: DRY-RUN — mọi thay đổi sẽ được ROLLBACK\n' >&2
else
  PAYLOAD="$SQL"
fi

case "$FORMAT" in
  table)
    printf '%s\n' "$PAYLOAD" | psql_run
    ;;
  tuples)
    printf '%s\n' "$PAYLOAD" | psql_run -At
    ;;
  csv)
    [ "$IS_WRITE" -eq 1 ] && die "-c chỉ dùng được với SELECT"
    CLEAN=$(printf '%s' "$SQL" | sed -e 's/;[[:space:]]*$//')
    printf '\\copy (%s) TO STDOUT WITH CSV HEADER\n' "$CLEAN" | psql_run -q
    ;;
  json)
    [ "$IS_WRITE" -eq 1 ] && die "-j chỉ dùng được với SELECT"
    CLEAN=$(printf '%s' "$SQL" | sed -e 's/;[[:space:]]*$//')
    printf 'SELECT coalesce(json_agg(row_to_json(_q)), '"'"'[]'"'"'::json) FROM (%s) _q;\n' "$CLEAN" \
      | psql_run -At
    ;;
esac