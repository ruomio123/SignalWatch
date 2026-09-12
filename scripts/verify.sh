#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
: "${M1_TEST_MYSQL_DSN:?set an isolated _test database DSN}"
: "${M4_TEST_SMTP_ADDR:?set isolated Mailpit address}"
: "${TEST_REDIS_ADDR:?set isolated Redis address}"
export SIGNALWATCH_INTEGRATION_REQUIRED=1
for tool in pdftotext pdfinfo pdfimages prlimit; do
  command -v "$tool" >/dev/null || { echo "missing PDF test dependency: $tool" >&2; exit 1; }
done
python3 scripts/check-architecture.py
test -z "$(gofmt -l cmd internal web/*.go)"
go vet ./...
go test -race -count=1 ./...
npm --prefix web run typecheck
npm --prefix web run format:check
npm --prefix web test
npm --prefix web run build
git diff --check
