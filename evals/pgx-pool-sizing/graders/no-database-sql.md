---
type: regex
target: { source: file, path: db.go }
pattern: '"database/sql"'
match: not_contains
---
