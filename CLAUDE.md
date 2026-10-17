# CLAUDE.md

This file guides Claude Code (and any other coding agent) when working inside this repository.

## What this repository is

`go-skills` is a collection of **opinionated skills** that teach coding agents to write idiomatic, production-ready Go. Each skill covers **one design decision** a Go developer faces (how to structure a project, how to handle errors, how to use context) and gives a clear position on it, with a good example, a bad example, and a review checklist.

It is **not** a style guide, not a tutorial, and not a collection of generic best practices. Every skill must say "do it this way, because of this" and show the cost of the alternative.

Primary audience: developers using Claude Code, Cursor, Codex or similar agents to write Go backend services. Secondary audience: Go developers reading the skills directly.

## Repository structure

```
go-skills/
├── CLAUDE.md                    # this file
├── README.md                    # what the repo is, how to install skills, index of skills (English)
├── README.pt-BR.md              # same content in Portuguese, kept in sync with README.md
├── LICENSE                      # MIT
├── CONTRIBUTING.md              # how to propose or change a skill (English)
├── CONTRIBUTING.pt-BR.md        # same content in Portuguese
├── .gitignore
│
├── .claude-plugin/
│   ├── plugin.json              # makes the repo a Claude Code plugin; bump "version" on every release
│   └── marketplace.json         # makes the repo its own marketplace (/plugin marketplace add rafapeixer/go-skills)
│
├── .claude/                     # for contributors working on this repo, not shipped to users
│   ├── settings.json            # allows go build/vet/test, gofmt and the scripts without prompts
│   └── skills/
│       ├── new-skill/           # /new-skill <name>: scaffold a skill from the template
│       └── review-skill/        # /review-skill <name>: check a skill against these rules
│
├── skills/                      # one folder per skill
│   ├── project-layout/
│   │   ├── SKILL.md             # the skill itself (frontmatter + instructions)
│   │   └── examples/
│   │       ├── good/            # minimal runnable Go code showing the right way
│   │       └── bad/             # minimal Go code showing the common mistake, with comments
│   ├── errors/
│   ├── context-and-deadlines/
│   ├── concurrency/
│   ├── http-server/
│   ├── postgres-pgx-pool/
│   ├── secure-defaults/
│   └── testing/
│
├── templates/
│   └── SKILL.template.md        # the template every new skill starts from
│
├── scripts/
│   ├── validate.sh              # format, Go floor (GO_FLOOR), excerpts match examples, README indexes
│   ├── test-examples.sh         # good: gofmt + go vet + go test (-race in CI); bad: build, tests compile
│   └── check-freshness.sh       # fails when the floor loses support or a skill is unverified for 6 months
│
├── .gitattributes               # LF everywhere, so scripts and excerpt checks behave the same on every OS
│
└── .github/
    ├── workflows/
    │   ├── ci.yml               # runs validate.sh and test-examples.sh on every push and PR
    │   └── freshness.yml        # runs check-freshness.sh on the 1st of every month
    ├── pull_request_template.md # what changes, what breaks without it, sources, checklist
    └── ISSUE_TEMPLATE/          # rule-change.md, new-skill.md
```

### Guardrails, not prose

Everything that can be checked is checked by a script, so a wrong change fails CI instead of relying on review:

| Mistake | Caught by |
|---|---|
| Missing or misordered section, wrong counts, bad frontmatter | `validate.sh` |
| `SKILL.md` excerpt that is not in the tested examples | `validate.sh` (every excerpt line must exist in `examples/good` or `examples/bad`) |
| Example on a Go version other than the floor | `validate.sh` (`GO_FLOOR`) |
| Example that does not compile, fails vet, races or fails tests | `test-examples.sh` |
| Floor older than the oldest supported Go release | `check-freshness.sh`, monthly |
| Facts not re-verified for more than six months | `check-freshness.sh`, via `metadata.verified` |

When you add a rule a script could enforce, add the check in the same PR.

### Evals

`evals/<case>/` holds `claude plugin eval` cases: a realistic prompt (`prompt.md`) and regex graders on the file the agent writes. Each case runs with and without the plugin, so the score delta shows whether the skills change what agents produce. Runs cost model calls; cap them:

```bash
claude plugin eval . --runs 2 --allow-tools Write --trust-plugin --no-publish --max-cost-usd 15
```

A new skill should come with at least one case whose graders fail on the mistake the skill prevents. `evals/results/` is gitignored.

The catalog is released in waves. Each skill is added in its own PR.

- **v1:** `project-layout`, `errors`, `context-and-deadlines`, `concurrency`, `http-server`, `postgres-pgx-pool`, `secure-defaults`, `testing`.
- **v1.1:** `sql-queries-and-transactions`, `db-migrations`, `logging-slog`, `ci-guardrails`, `container-runtime-tuning`, `overload-protection`, `outbound-http-client`, `go-version-policy`.
- **v2:** `observability-otel`, `performance-profiling`, `sqlite-single-node`, `background-jobs-outbox`, `scale-to-zero`, `json-v2`, `wasm-plugins`, `mcp-server-go`.

Deliberately out of the catalog until they mature: Go services as WASI components (no `wasip2` port), writing eBPF programs, eBPF auto-instrumentation as a default, `runtime/secret`, SIMD, arenas, carbon-aware scheduling, OTel profiles.

The guiding idea for every skill: in current Go, production readiness is mostly about **not switching off good defaults** (post-quantum TLS, container-aware `GOMAXPROCS`, pgx extended protocol) and about **bounding everything** (timeouts, body sizes, pools, goroutines, retries).

## Skill format

Every `SKILL.md` follows the Agent Skills convention: YAML frontmatter followed by Markdown instructions. The frontmatter is what agents use to decide when to load the skill, so the `description` must say **when to use it**, not just what it is.

```markdown
---
name: errors
description: Use when writing or reviewing Go code that creates, wraps, returns, inspects or logs errors. Covers wrapping with context, sentinel vs typed errors, errors.Is/As, and what to log vs return.
metadata:
  verified: 2026-10-17
---
```

`metadata.verified` is the date the skill's facts (APIs, defaults, versions) were last checked against primary sources. Update it whenever you re-check or change facts; the monthly freshness job fails once it is more than six months old.

Required sections, in this order:

1. **The decision** — one paragraph naming the decision this skill covers and why it matters.
2. **The rule** — the position this repository takes, stated directly. Numbered rules, each one sentence.
3. **Why** — the reasoning behind the rule and the cost of the alternative. Short.
4. **Good example** — a reference to `examples/good/` with a 5 to 15 line excerpt inline and a one-line explanation.
5. **Bad example** — a reference to `examples/bad/` with an excerpt inline and a comment on each mistake.
6. **Checklist** — 5 to 10 yes/no items (`- [ ] ...`) an agent applies when reviewing code. This is the only copy of the checklist; there is no separate checklist file, so it cannot drift.
7. **When not to apply** — the cases where the rule bends. Every rule has them; a skill with no exceptions is not trustworthy.

Length target: 100 to 400 lines per `SKILL.md`. Never pad a skill to reach the minimum; if it grows past the maximum, split it into two skills.

## Rules for working in this repo

### Writing skills

- **One decision per skill.** If a skill covers two decisions, split it.
- **Take a position.** Never write "it depends" without immediately saying on what, and what to do in each case.
- **Every claim needs a reason.** "Use `errors.Is`" is not enough; say what breaks when you compare with `==`.
- **Examples must compile.** Good examples must also be gofmt-clean and pass `go vet` and `go test`. Bad examples only need `go build`, because some of their mistakes are exactly what `go vet` reports. A good example is a tiny `main.go` or `_test.go`, never a snippet that cannot run.
- **Bad examples are real mistakes**, the ones agents and junior developers actually make, not strawmen. Comment each mistake inline.
- **Prefer the standard library.** Mention a third-party package only when the standard library has no reasonable answer (for example `pgx` for Postgres, `errgroup` for concurrent error handling). Say why.
- **Target supported Go.** The floor is the oldest Go release that still receives security fixes: today `go 1.26.0`. Every example declares it, and CI tests against 1.26 and the latest stable release with `GOTOOLCHAIN=local`. Raise the floor every six months, when a new Go release ships and the oldest one loses support: change `GO_FLOOR` in `scripts/validate.sh`, every example `go.mod`, this file and the READMEs in one commit, then run `go fix ./...` in every example.
- **Version-gate newer features.** A rule that needs a release newer than the floor says so inline, for example "(Go 1.27+)", because an agent following it on the floor would generate code that does not compile.
- Use what the floor gives you: `errors.AsType`, `sync.WaitGroup.Go`, `testing/synctest`, `t.Context()`, `b.Loop()`, `os.Root`, `crypto/rand.Text`, `http.CrossOriginProtection`, `new(expr)`, iterators. Do not teach workarounds for older versions.
- **Dependencies have floors too.** pgx `>= v5.9.2`, because earlier versions lack the fix for a simple-protocol SQL injection. State the minimum version whenever a rule depends on a third-party package.
- **No filler.** No introductions about how important the topic is. Start at the decision.

### Language and tone

- Skills, templates and code comments are in **English**. The audience is global.
- The project explanation is **bilingual: English and Portuguese (pt-BR)**. `README.md` is the English version and `README.pt-BR.md` is the Portuguese one. Both start with a language switcher (`English | Português`) linking to each other.
- Any change to `README.md` updates `README.pt-BR.md` in the same commit, and vice versa. The two must never drift: same sections, same order, same install commands, same skill index.
- `CONTRIBUTING.md` follows the same rule (`CONTRIBUTING.pt-BR.md`).
- Translate meaning, not word by word. Keep code, commands, file names and Go identifiers untranslated.
- Direct, plain, no marketing words. "Do X" rather than "it is generally recommended to consider X".
- Second person when giving instructions ("wrap the error with the operation name").

### Changing existing skills

- Read the whole `SKILL.md` and its examples before editing.
- Changing a rule is a breaking change for people who adopted it: open an issue first, explain what was wrong, and update the examples and checklist in the same PR.
- Never weaken a rule to accommodate a bad example; fix the example.

### Code in examples

- Module path per example: `github.com/rafapeixer/go-skills/skills/<skill>/examples/<good|bad>`.
- Each example has its own `go.mod` so it runs in isolation.
- Keep examples under 100 lines. They illustrate one point; they are not applications.
- Run `scripts/validate.sh` and `scripts/test-examples.sh` before committing. CI runs both and rejects failures.
- While a skill is in progress, the README lists it without a link. The PR that adds the skill turns its name into a link, in both READMEs.

### Commits and PRs

- One skill per PR. One logical change per commit.
- Commit message format: `skill(errors): add rule on wrapping with operation name` or `docs(readme): update skill index`.
- A PR adding a skill must also add it to the README index, in both `README.md` and `README.pt-BR.md`.

## How agents should use this repo as a consumer

When a user has installed these skills and is writing Go code, the agent should:

1. Load the skill whose `description` matches the task (writing a handler → `http-server`; configuring the database pool → `postgres-pgx-pool`).
2. Apply **The rule** section while writing.
3. Run the **Checklist** section against the result before presenting it.
4. If the task falls under **When not to apply**, say so explicitly rather than silently ignoring the rule.

## Non-goals

- Not a Go tutorial. Assume the reader knows the language.
- Not a linter configuration. Skills are about design decisions, not formatting.
- Not framework-specific. No Gin, Echo, Fiber or GORM skills; the standard library plus `pgx` is the baseline.
- Not a place for personal preferences without justification. Every rule must survive the question "what breaks if I don't?".
