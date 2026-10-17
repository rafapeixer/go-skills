---
type: regex
target: { source: file, path: user.go }
pattern: 'err\.Error\(\)\s*=='
match: not_contains
---
