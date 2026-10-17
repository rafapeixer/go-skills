#!/usr/bin/env bash
# Checks that every skill follows the format described in CLAUDE.md:
# frontmatter, required sections in order, length, rule and checklist counts,
# example folders, and presence in both README indexes.
set -euo pipefail

cd "$(dirname "$0")/.."

SECTIONS=("The decision" "The rule" "Why" "Good example" "Bad example" "Checklist" "When not to apply")
MIN_LINES=100
MAX_LINES=400

errors=0
fail() {
  echo "FAIL $1: $2"
  errors=$((errors + 1))
}

# section_body FILE TITLE prints the lines between "## TITLE" and the next "## ".
section_body() {
  awk -v title="## $2" '
    $0 == title { inside = 1; next }
    /^## / && inside { exit }
    inside { print }
  ' "$1"
}

shopt -s nullglob
skills=(skills/*/)

if [ ${#skills[@]} -eq 0 ]; then
  echo "no skills yet, nothing to validate"
fi

for dir in "${skills[@]}"; do
  skill=$(basename "$dir")
  file="$dir/SKILL.md"

  if [ ! -f "$file" ]; then
    fail "$skill" "missing SKILL.md"
    continue
  fi

  # Frontmatter: first line is ---, then name and description, then ---.
  if [ "$(head -n 1 "$file")" != "---" ]; then
    fail "$skill" "SKILL.md must start with YAML frontmatter (---)"
  fi
  frontmatter=$(awk 'NR == 1 { next } /^---$/ { exit } { print }' "$file")
  name=$(printf '%s\n' "$frontmatter" | sed -n 's/^name: *//p')
  description=$(printf '%s\n' "$frontmatter" | sed -n 's/^description: *//p')

  if [ "$name" != "$skill" ]; then
    fail "$skill" "frontmatter name '$name' does not match folder name"
  fi
  if [[ "$description" != "Use when "* ]]; then
    fail "$skill" "description must start with 'Use when '"
  fi
  if [ ${#description} -gt 1024 ]; then
    fail "$skill" "description is longer than 1024 characters"
  fi

  # Required sections, exactly these, in this order.
  actual=$(grep '^## ' "$file" | sed 's/^## //')
  expected=$(printf '%s\n' "${SECTIONS[@]}")
  if [ "$actual" != "$expected" ]; then
    fail "$skill" "sections must be, in order: $(printf '%s, ' "${SECTIONS[@]}" | sed 's/, $//')"
  fi

  lines=$(wc -l < "$file" | tr -d ' ')
  if [ "$lines" -lt "$MIN_LINES" ] || [ "$lines" -gt "$MAX_LINES" ]; then
    fail "$skill" "SKILL.md has $lines lines, expected $MIN_LINES to $MAX_LINES"
  fi

  rules=$(section_body "$file" "The rule" | grep -cE '^[0-9]+\. ' || true)
  if [ "$rules" -lt 4 ] || [ "$rules" -gt 8 ]; then
    fail "$skill" "The rule has $rules numbered rules, expected 4 to 8"
  fi

  items=$(section_body "$file" "Checklist" | grep -c '^- \[ \] ' || true)
  if [ "$items" -lt 5 ] || [ "$items" -gt 10 ]; then
    fail "$skill" "Checklist has $items items, expected 5 to 10"
  fi

  exceptions=$(section_body "$file" "When not to apply" | grep -c '^- ' || true)
  if [ "$exceptions" -lt 1 ]; then
    fail "$skill" "When not to apply has no cases"
  fi

  for kind in good bad; do
    if [ ! -f "$dir/examples/$kind/go.mod" ]; then
      fail "$skill" "missing examples/$kind/go.mod"
    fi
  done

  for readme in README.md README.pt-BR.md; do
    if ! grep -q "(skills/$skill/SKILL.md)" "$readme"; then
      fail "$skill" "not listed in $readme"
    fi
  done
done

# The two READMEs must keep the same structure.
en=$(grep -c '^## ' README.md || true)
pt=$(grep -c '^## ' README.pt-BR.md || true)
if [ "$en" != "$pt" ]; then
  fail "README" "README.md has $en sections, README.pt-BR.md has $pt"
fi

if [ "$errors" -gt 0 ]; then
  echo "$errors problem(s) found"
  exit 1
fi
echo "ok"
