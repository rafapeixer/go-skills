---
description: "Production HTTP server: timeouts, shutdown, body limits"
tags: [code]
runs: 2
max_turns: 15
allowed_tools: [Read, Glob, Grep, Skill, Write]
---
Write main.go for a production HTTP server for a small JSON API with `GET /users/{id}` and `POST /users`, using only the Go standard library. Keep users in memory. Write the file; no explanation needed.
