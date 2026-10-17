---
description: "Postgres pool sized to the database budget"
tags: [code]
runs: 2
max_turns: 15
allowed_tools: [Read, Glob, Grep, Skill, Write]
---
Write db.go in package db with a function that creates the Postgres connection pool for our Go service using pgx v5. We run 6 replicas (up to 8 during rolling deploys) and the database gives this service 200 connections in total. Write the file; no explanation needed.
