#!/usr/bin/env bash
# Run the maintained paper contract tests with fake models and no application env.
set -euo pipefail
if [[ ${1:-} != --isolated ]]; then
  exec env -i PATH="$PATH" HOME="$HOME" LANG=C.UTF-8 bash "$0" --isolated "$@"
fi
shift
cd "$(dirname "$0")/.."
umask 077
export GOCACHE="$PWD/.cache/go-build"
export GOMODCACHE="$PWD/.cache/go-mod"
export GOTMPDIR="$PWD/.cache/tmp"
export TMPDIR="$GOTMPDIR"
mkdir -p "$GOCACHE" "$GOMODCACHE" "$GOTMPDIR"
chmod 700 .cache

# Keep ignored historical tests and their unrelated dependencies out of this
# acceptance entry point. Every selected test is self-contained and tracked.
for test_file in \
  internal/document/paper_retrieval_test.go \
  internal/document/structured_test.go \
  internal/platform/document/structured_html_test.go \
  internal/generation/paper_schema_test.go \
  internal/generation/paper_budget_test.go \
  internal/platform/config/paper_budget_test.go \
  internal/ai/paper_budget_test.go \
  internal/bootstrap/paper_budget_test.go \
  internal/platform/llm/paper_budget_test.go; do
  package_dir="${test_file%/*}"
  mapfile -t source_files < <(rg --files "$package_dir" -g '*.go' -g '!*_test.go' | sort)
  echo "Testing $package_dir"
  go test -race -count=1 "$@" "${source_files[@]}" "$test_file"
done
