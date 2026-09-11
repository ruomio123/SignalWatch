#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly PROJECT_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)"
readonly ENV_FILE="${ENV_FILE:-$PROJECT_ROOT/.env}"
readonly COMPOSE_FILE="${COMPOSE_FILE:-$PROJECT_ROOT/deploy/compose.yaml}"
readonly API_BASE_URL="${API_BASE_URL:-http://127.0.0.1:8080}"
readonly REGISTER_URL="${API_BASE_URL%/}/api/v2/auth/register"
readonly TEST_PASSWORD="correct-horse-123"
readonly TEST_EMAIL="race-$(date +%s%N)-$$@example.com"

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

require_command curl
require_command docker
require_command mktemp

[[ -f "$ENV_FILE" ]] || fail "environment file not found: $ENV_FILE"
[[ -f "$COMPOSE_FILE" ]] || fail "Compose file not found: $COMPOSE_FILE"
docker compose version >/dev/null 2>&1 || fail "docker compose is unavailable"

readonly RESULT_DIR="$(mktemp -d)"

cleanup() {
  rm -f -- \
    "$RESULT_DIR/request-1.body" \
    "$RESULT_DIR/request-1.error" \
    "$RESULT_DIR/request-1.status" \
    "$RESULT_DIR/request-2.body" \
    "$RESULT_DIR/request-2.error" \
    "$RESULT_DIR/request-2.status" \
    "$RESULT_DIR/start-gate"
  rmdir -- "$RESULT_DIR" 2>/dev/null || true
}
trap cleanup EXIT

printf -v REQUEST_BODY \
  '{"email":"%s","password":"%s"}' \
  "$TEST_EMAIL" \
  "$TEST_PASSWORD"

send_request() {
  local request_number="$1"

  while [[ ! -e "$RESULT_DIR/start-gate" ]]; do
    sleep 0.01
  done

  curl --noproxy '*' --silent --show-error \
    --output "$RESULT_DIR/request-$request_number.body" \
    --write-out '%{http_code}\n' \
    --request POST "$REGISTER_URL" \
    --header 'Content-Type: application/json' \
    --data "$REQUEST_BODY" \
    >"$RESULT_DIR/request-$request_number.status" \
    2>"$RESULT_DIR/request-$request_number.error"
}

printf 'Concurrent registration acceptance test\n'
printf 'URL:   %s\n' "$REGISTER_URL"
printf 'Email: %s\n\n' "$TEST_EMAIL"

# 两个后台任务先等待同一个起跑信号，再尽可能同时发出请求。
send_request 1 &
readonly REQUEST_1_PID=$!
send_request 2 &
readonly REQUEST_2_PID=$!
: >"$RESULT_DIR/start-gate"

set +e
wait "$REQUEST_1_PID"
request_1_exit_code=$?
wait "$REQUEST_2_PID"
request_2_exit_code=$?
set -e

if ((request_1_exit_code != 0)); then
  sed -n '1,5p' "$RESULT_DIR/request-1.error" >&2
  fail "request 1 failed with curl exit code $request_1_exit_code"
fi
if ((request_2_exit_code != 0)); then
  sed -n '1,5p' "$RESULT_DIR/request-2.error" >&2
  fail "request 2 failed with curl exit code $request_2_exit_code"
fi

readonly STATUS_1="$(tr -d '\r\n' <"$RESULT_DIR/request-1.status")"
readonly STATUS_2="$(tr -d '\r\n' <"$RESULT_DIR/request-2.status")"

for request_number in 1 2; do
  status_variable="STATUS_$request_number"
  printf 'Request %s: HTTP %s\n' "$request_number" "${!status_variable}"
  sed -n '1p' "$RESULT_DIR/request-$request_number.body"
  printf '\n'
done

if [[ "$STATUS_1:$STATUS_2" != "201:409" && "$STATUS_1:$STATUS_2" != "409:201" ]]; then
  fail "expected one HTTP 201 and one HTTP 409, got $STATUS_1 and $STATUS_2"
fi

if [[ "$STATUS_1" == "201" ]]; then
  readonly SUCCESS_BODY="$RESULT_DIR/request-1.body"
  readonly CONFLICT_BODY="$RESULT_DIR/request-2.body"
else
  readonly SUCCESS_BODY="$RESULT_DIR/request-2.body"
  readonly CONFLICT_BODY="$RESULT_DIR/request-1.body"
fi

grep -Fq "\"email\":\"$TEST_EMAIL\"" "$SUCCESS_BODY" || \
  fail "HTTP 201 response does not contain the normalized test email"
grep -Fq '"code":"EMAIL_ALREADY_REGISTERED"' "$CONFLICT_BODY" || \
  fail "HTTP 409 response does not contain EMAIL_ALREADY_REGISTERED"

for response_body in "$SUCCESS_BODY" "$CONFLICT_BODY"; do
  if grep -Fqi "$TEST_PASSWORD" "$response_body" || grep -Fqi 'password_hash' "$response_body"; then
    fail "HTTP response exposes password data"
  fi
done

readonly SQL="
SELECT
    COUNT(*),
    COALESCE(MIN(LEFT(password_hash, 3)), ''),
    COALESCE(MIN(password_hash <> '$TEST_PASSWORD'), 0),
    COALESCE(MIN(timezone), ''),
    COALESCE(MIN(TIME_FORMAT(digest_time, '%H:%i:%s')), ''),
    COALESCE(MIN(max_items_per_digest), 0),
    COALESCE(MIN(status), '')
FROM users
WHERE email = '$TEST_EMAIL';
"

set +e
database_result="$(
  cd "$PROJECT_ROOT" &&
    docker compose --env-file "$ENV_FILE" -f "$COMPOSE_FILE" exec -T mysql \
      sh -c 'export MYSQL_PWD="$MYSQL_PASSWORD"; exec mysql --batch --skip-column-names --user="$MYSQL_USER" "$MYSQL_DATABASE" --execute="$1"' \
      sh "$SQL"
)"
database_exit_code=$?
set -e

((database_exit_code == 0)) || fail "database verification failed"

IFS=$'\t' read -r \
  row_count \
  hash_prefix \
  not_plaintext \
  timezone \
  digest_time \
  max_items_per_digest \
  user_status \
  <<<"$database_result"

[[ "$row_count" == "1" ]] || fail "expected one database row, got ${row_count:-no result}"
[[ "$not_plaintext" == "1" ]] || fail "stored password hash equals the plaintext password"
case "$hash_prefix" in
  '$2a'|'$2b'|'$2y') ;;
  *) fail "stored password does not have a recognized bcrypt prefix: ${hash_prefix:-empty}" ;;
esac
[[ "$timezone" == "UTC" ]] || fail "expected timezone UTC, got ${timezone:-empty}"
[[ "$digest_time" == "08:00:00" ]] || fail "expected digest_time 08:00:00, got ${digest_time:-empty}"
[[ "$max_items_per_digest" == "50" ]] || \
  fail "expected max_items_per_digest 50, got ${max_items_per_digest:-empty}"
[[ "$user_status" == "active" ]] || fail "expected status active, got ${user_status:-empty}"

printf 'Database: row_count=%s, hash_prefix=%s, not_plaintext=%s\n' \
  "$row_count" \
  "$hash_prefix" \
  "$not_plaintext"
printf 'Defaults: timezone=%s, digest_time=%s, max_items_per_digest=%s, status=%s\n' \
  "$timezone" \
  "$digest_time" \
  "$max_items_per_digest" \
  "$user_status"
printf '\nPASS: concurrent registration created exactly one user and returned HTTP 201 + 409.\n'
