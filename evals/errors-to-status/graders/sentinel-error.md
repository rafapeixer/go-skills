---
type: regex
target: { source: file, path: user.go }
pattern: 'var\s+Err\w+\s*=\s*errors\.New'
match: contains
---
