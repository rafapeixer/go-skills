# go-skills

**English** | [Português](README.pt-BR.md)

**Opinionated skills that teach coding agents to write idiomatic, production-ready Go.**

Coding agents know Go syntax. They don't know *which* way to do things when the language offers several. Left alone, they write Go that looks like Java or Python: fat service layers, errors swallowed or over-wrapped, goroutines nobody owns, mocks for everything.

Each skill here covers **one design decision**, takes a clear position on it, shows a good example and a bad one, and gives the agent a checklist to review its own output. Every example compiles and is tested in CI.

Works with Claude Code, Cursor, Codex and any agent that reads `SKILL.md` files.

---

## Install

### Claude Code plugin (recommended)

```
/plugin marketplace add rafapeixer/go-skills
/plugin install go-skills@go-skills
```

Skills load automatically when the task matches, or by name: `/go-skills:errors`. Updates arrive with each release.

### Claude Code, copying the files

Project-level (versioned with your repo):

```bash
git clone https://github.com/rafapeixer/go-skills.git /tmp/go-skills
mkdir -p .claude/skills
cp -r /tmp/go-skills/skills/* .claude/skills/
```

Personal (available in every project):

```bash
cp -r /tmp/go-skills/skills/* ~/.claude/skills/
```

### Universal skills CLI

If you use the `skills` CLI, which supports most agents:

```bash
npx skills add rafapeixer/go-skills
```

### Any other agent

Copy the `skills/` folder into wherever your agent loads skills from, or point it at this repository. Each skill is a self-contained folder with a `SKILL.md` at its root.

---

## Skills

> v1, v1.1 and v2 are ready: 24 skills. New skills are added one per PR.

### v1

| Skill | Use when | Status |
|---|---|---|
| [project-layout](skills/project-layout/SKILL.md) | Creating a service, adding a package, placing an interface or helper, deciding whether to split into services | Ready |
| [errors](skills/errors/SKILL.md) | Creating, wrapping, returning or inspecting errors | Ready |
| [context-and-deadlines](skills/context-and-deadlines/SKILL.md) | Passing or creating a `context.Context`, setting request or outbound timeouts, running work after the response | Ready |
| [concurrency](skills/concurrency/SKILL.md) | Starting goroutines, fanning work out over a list, running background workers, sharing state between goroutines | Ready |
| [http-server](skills/http-server/SKILL.md) | Building, hardening or shutting down a net/http server: timeouts, routing, body limits, health checks, CSRF | Ready |
| [postgres-pgx-pool](skills/postgres-pgx-pool/SKILL.md) | Choosing the Postgres driver, sizing the pgx pool, putting PgBouncer in front, handling pool exhaustion | Ready |
| [secure-defaults](skills/secure-defaults/SKILL.md) | Generating tokens, opening user-supplied paths, configuring TLS, encrypting, rendering HTML, setting cookies | Ready |
| [testing](skills/testing/SKILL.md) | Writing or reviewing tests: test doubles, database tests, time-dependent code, end-to-end HTTP, fuzzing, benchmarks | Ready |

### v1.1

| Skill | Use when | Status |
|---|---|---|
| [sql-queries-and-transactions](skills/sql-queries-and-transactions/SKILL.md) | Writing Postgres queries or transactions with pgx: sqlc, keyset pagination, N+1, retrying serialization failures, outbox | Ready |
| [db-migrations](skills/db-migrations/SKILL.md) | Adding schema migrations, choosing where they run during a deploy, changing a table under live traffic | Ready |
| [logging-slog](skills/logging-slog/SKILL.md) | Logging with log/slog: injected logger, request IDs via context, secret redaction, JSON keys, levels and sampling | Ready |
| [ci-guardrails](skills/ci-guardrails/SKILL.md) | Writing CI pipelines or agent hooks: gofmt/vet/go fix/race gates, govulncheck, golangci-lint v2, tool pinning, release builds | Ready |
| [container-runtime-tuning](skills/container-runtime-tuning/SKILL.md) | Writing a Dockerfile or Kubernetes manifest, setting CPU and memory limits, choosing GOMAXPROCS, GOMEMLIMIT or GOGC | Ready |
| [overload-protection](skills/overload-protection/SKILL.md) | Deciding what the service does above capacity: limiting in-flight requests, shedding with 503, retries, bulkheads | Ready |
| [outbound-http-client](skills/outbound-http-client/SKILL.md) | Calling another service over HTTP: building the client and transport, reading bodies, fetching user-supplied URLs | Ready |
| [go-version-policy](skills/go-version-policy/SKILL.md) | Choosing the `go` and `toolchain` lines, upgrading Go, setting `GOTOOLCHAIN` in CI, pinning tools | Ready |

### v2

| Skill | Use when | Status |
|---|---|---|
| [observability-otel](skills/observability-otel/SKILL.md) | Adding OpenTelemetry traces, metrics or logs: SDK setup and shutdown, otelhttp, span names and cardinality, sampling, views, slog bridge | Ready |
| [performance-profiling](skills/performance-profiling/SKILL.md) | Measuring before optimizing: pprof on an internal listener, flight recorder snapshots, default.pgo, escape analysis, sync.Pool, per-request cost | Ready |
| [sqlite-single-node](skills/sqlite-single-node/SKILL.md) | Using SQLite as a single-node service's database: driver, per-connection pragmas, one writer and a read pool, BEGIN IMMEDIATE, Litestream | Ready |
| [background-jobs-outbox](skills/background-jobs-outbox/SKILL.md) | Running work after a write: River InsertTx or an outbox, SKIP LOCKED workers, LISTEN/NOTIFY, retries, worker shutdown | Ready |
| [scale-to-zero](skills/scale-to-zero/SKILL.md) | Running on Lambda, Cloud Run, Knative or Fly Machines: startup cost, lazy dependencies, SIGTERM within the grace period, Lambda builds | Ready |
| [json-v2](skills/json-v2/SKILL.md) | Marshaling or decoding JSON, omitzero vs omitempty, custom marshalers, upgrading to Go 1.27 and encoding/json/v2 | Ready |
| [wasm-plugins](skills/wasm-plugins/SKILL.md) | Letting third parties extend the service: wazero instead of plugin or exec, wasip1 guests, capabilities, CPU and memory limits | Ready |
| [mcp-server-go](skills/mcp-server-go/SKILL.md) | Exposing a Go service to agents over MCP: official go-sdk, typed tools, stateless streamable HTTP, OAuth bearer tokens, per-call limits | Ready |

One skill per PR; see [CONTRIBUTING.md](CONTRIBUTING.md).

---

## What a skill looks like

Every `SKILL.md` has the same shape:

1. **The decision** — what choice this skill is about
2. **The rule** — the position this repo takes, as numbered one-line rules
3. **Why** — the reasoning and the cost of doing it differently
4. **Good example** — runnable code in `examples/good/`
5. **Bad example** — the real mistake, in `examples/bad/`, with each error commented
6. **Checklist** — yes/no items the agent applies when reviewing
7. **When not to apply** — the exceptions, because every rule has them

Skills are deliberately short (100 to 400 lines). If one grows past that, it gets split.

---

## Principles

- **One decision per skill.** No catch-all "best practices".
- **A position, with reasons.** "It depends" is only allowed when followed by "on what, and here is what to do in each case".
- **Standard library first.** Third-party packages appear only where the standard library has no reasonable answer (`pgx`, `errgroup`), and the skill says why.
- **Current Go.** `log/slog`, generics, `errors.Join`, range-over-func. Examples require Go 1.26 or later (the oldest release still receiving security fixes); CI also tests against the latest stable release.
- **Examples run.** CI executes `gofmt`, `go vet` and `go test` on every good example, and `go build` on every bad one.

---

## Contributing

Disagree with a rule? Open an issue and make the case. Rules change when someone shows what breaks, not when someone prefers something else.

Want to add a skill? Start from [templates/SKILL.template.md](templates/SKILL.template.md), follow [CLAUDE.md](CLAUDE.md), one skill per PR.

---

## License

MIT
