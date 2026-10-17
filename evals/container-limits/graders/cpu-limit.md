---
type: regex
target: { source: file, path: deployment.yaml }
pattern: 'limits:[\s\S]*cpu:'
match: contains
---
