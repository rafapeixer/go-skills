#!/usr/bin/env bash
# Good examples must be gofmt-clean, pass go vet and pass go test.
# Bad examples, test files included, only need to compile: they contain mistakes on purpose,
# and some of those mistakes are exactly what go vet reports.
set -euo pipefail

cd "$(dirname "$0")/.."

errors=0
fail() {
  echo "FAIL $1"
  errors=$((errors + 1))
}

shopt -s nullglob
good=(skills/*/examples/good/)
bad=(skills/*/examples/bad/)

if [ ${#good[@]} -eq 0 ] && [ ${#bad[@]} -eq 0 ]; then
  echo "no examples yet, nothing to test"
  exit 0
fi

for dir in "${good[@]}"; do
  echo "== $dir"
  unformatted=$(gofmt -l "$dir")
  if [ -n "$unformatted" ]; then
    fail "$dir: not gofmt-clean: $unformatted"
  fi
  (cd "$dir" && go vet ./...) || fail "$dir: go vet"
  # RACE=1 adds the race detector; it needs cgo, so CI sets it and local runs may not.
  (cd "$dir" && go test ${RACE:+-race} ./...) || fail "$dir: go test"
done

bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
for dir in "${bad[@]}"; do
  echo "== $dir"
  (cd "$dir" && go build -o "$bin/" ./...) || fail "$dir: go build"
  # Test files in bad examples must compile too; vet is off because the mistakes are deliberate.
  (cd "$dir" && go test -vet=off -run '^$' ./... >/dev/null) || fail "$dir: test files do not compile"
done

if [ "$errors" -gt 0 ]; then
  echo "$errors problem(s) found"
  exit 1
fi
echo "ok"
