---
type: regex
target: { source: file, path: deployment.yaml }
pattern: 'name:\s*"?GOMAXPROCS'
match: not_contains
---
