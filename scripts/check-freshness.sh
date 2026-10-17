#!/usr/bin/env bash
# Fails when the repository has started teaching outdated Go:
#   - the Go floor is older than the oldest release that still gets security fixes;
#   - a skill's facts were last verified more than MAX_AGE_DAYS ago.
# Also reports (without failing) direct dependencies of examples that have newer versions.
# Needs network access; CI runs it on a schedule.
set -euo pipefail

cd "$(dirname "$0")/.."

MAX_AGE_DAYS=183
floor=$(sed -n 's/^GO_FLOOR=//p' scripts/validate.sh)

errors=0
fail() {
  echo "FAIL $1"
  errors=$((errors + 1))
}

# go.dev lists exactly the supported releases (the two newest majors) in this feed.
supported=$(curl -fsSL 'https://go.dev/dl/?mode=json' | grep -o '"version": *"go[0-9.]*"' | grep -o '[0-9][0-9.]*' | sort -uV)
oldest=$(printf '%s\n' "$supported" | cut -d. -f1,2 | sort -uV | head -n 1)
echo "supported Go releases: $(printf '%s ' $supported)"
if [ "$(printf '%s\n%s\n' "${floor%.*}" "$oldest" | sort -V | head -n 1)" != "$oldest" ]; then
  fail "Go floor $floor is older than the oldest supported release $oldest: raise it (see go-version-policy)"
fi

now=$(date -u +%s)
for file in skills/*/SKILL.md; do
  verified=$(sed -n 's/^  verified: *//p' "$file" | head -n 1)
  age=$(( (now - $(date -u -d "$verified" +%s)) / 86400 ))
  if [ "$age" -gt "$MAX_AGE_DAYS" ]; then
    fail "$file: facts last verified $verified ($age days ago); re-check them and update metadata.verified"
  fi
done

for mod in skills/*/examples/*/go.mod; do
  dir=$(dirname "$mod")
  updates=$(cd "$dir" && go list -m -u -f '{{if and (not .Indirect) (not .Main) .Update}}{{.Path}} {{.Version}} -> {{.Update.Version}}{{end}}' all 2>/dev/null || true)
  if [ -n "$updates" ]; then
    printf 'WARN %s has newer dependencies:\n%s\n' "$dir" "$updates"
  fi
done

if [ "$errors" -gt 0 ]; then
  echo "$errors problem(s) found"
  exit 1
fi
echo "ok"
