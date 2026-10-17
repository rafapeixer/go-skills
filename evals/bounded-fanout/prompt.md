---
description: "Bounded concurrent fan-out that stops on first error"
tags: [code]
runs: 2
max_turns: 15
allowed_tools: [Read, Glob, Grep, Skill, Write]
---
Write prices.go: a function that fetches prices for up to 10,000 product IDs from an HTTP pricing API concurrently, and stops all outstanding requests as soon as one fails. Write the file; no explanation needed.
