---
name: review-skill
description: Review one go-skills skill against the repository rules and current Go facts. Use before opening a PR for a new or changed skill.
argument-hint: <skill-name>
disable-model-invocation: true
---

# Review skill

Review `skills/$ARGUMENTS/` against `CLAUDE.md`. Report findings as a list, most severe first; do not edit unless asked.

Check:

1. **Format.** `bash scripts/validate.sh` and `bash scripts/test-examples.sh` pass.
2. **One decision.** The skill covers a single decision; if it covers two, say where to split.
3. **Position and reasons.** Every rule is imperative, one sentence, and the "Why" section says what breaks without it. Flag any "it depends" that does not say on what.
4. **Facts are current.** Every API, version and default named in the skill exists and behaves as described in the Go release that the examples' `go` directive targets. Rules that need a newer release carry an inline version gate. Verify doubtful claims against go.dev release notes and pkg.go.dev.
5. **Examples.** The good example is the smallest code that shows the rules. The bad example contains mistakes people actually make, each marked `// MISTAKE:`. The inline excerpts match the files.
6. **Checklist.** 5 to 10 yes/no items that a reviewer can answer by reading code.
7. **Exceptions.** "When not to apply" lists real cases and what to do instead.
8. **Defaults.** The skill does not tell readers to switch off a good default (post-quantum TLS curves, container-aware `GOMAXPROCS`, pgx extended protocol) without a stated reason.
