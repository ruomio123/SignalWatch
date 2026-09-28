#!/usr/bin/env bash
# Run only against our isolated Compose MySQL, never an application DSN.
set -euo pipefail
if [[ ${1:-} != --isolated ]]; then
  exec env -i PATH="$PATH" HOME="$HOME" LANG=C.UTF-8 bash "$0" --isolated "$@"
fi
shift
cd "$(dirname "$0")/.."
export GOCACHE="$PWD/.cache/go-build"
export GOMODCACHE="$PWD/.cache/go-mod"
export GOTMPDIR="$PWD/.cache/tmp"
export TMPDIR="$GOTMPDIR"
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR"
chmod 700 .cache
# The dedicated port is shared by local tests; never run two fixtures at once.
exec 9>.cache/agent-test.lock
flock -n 9 || { echo 'another Agent test run is active' >&2; exit 1; }
compose=(docker compose -p signalwatch-refactor-test --env-file /dev/null -f deploy/compose.test.yaml)
"${compose[@]}" up -d --wait mysql
mysql() { "${compose[@]}" exec -T -e MYSQL_PWD=isolated-test-only mysql mysql -uroot "$@"; }
test_db="signalwatch_agent_${$}_${RANDOM}_test"
mysql -e "CREATE DATABASE $test_db"
cleanup() { mysql -e "DROP DATABASE IF EXISTS $test_db"; }
trap cleanup EXIT
export M1_TEST_MYSQL_DSN="root:isolated-test-only@tcp(127.0.0.1:13306)/${test_db}?parseTime=true&loc=UTC"
export SIGNALWATCH_INTEGRATION_REQUIRED=1
goose -dir migrations mysql "$M1_TEST_MYSQL_DSN" up
go test -race -count=1 ./internal/agent "$@"
