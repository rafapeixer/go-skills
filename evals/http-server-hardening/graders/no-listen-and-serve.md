---
type: regex
target: { source: file, path: main.go }
pattern: 'http\.ListenAndServe(TLS)?\('
match: not_contains
---
