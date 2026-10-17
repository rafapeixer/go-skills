---
description: "Sentinel errors mapped to HTTP status"
tags: [code]
runs: 2
max_turns: 15
allowed_tools: [Read, Glob, Grep, Skill, Write]
---
Write user.go: a UserStore backed by a map with a GetUser(id string) method, and an HTTP handler for `GET /users/{id}` that returns 404 when the user does not exist and 500 for any other error. Standard library only. Write the file; no explanation needed.
