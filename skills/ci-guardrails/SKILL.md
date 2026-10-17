---
name: ci-guardrails
description: Use when writing or reviewing a CI pipeline, pre-commit hook or coding-agent hook for a Go module, a .golangci.yml, or a release build step. Covers gofmt, go vet, go fix -diff modernizers and go test -race as gates, the govulncheck exit-code trap and binary-mode scans, golangci-lint v2 config with govet, staticcheck, errcheck, gosec and bodyclose, pinning tools with tool directives, #nosec and nolint hygiene, GOSUMDB/GOPRIVATE/go.sum, and CGO_ENABLED=0 -trimpath release builds.
---

# CI guardrails

## The decision

Every Go repository needs a pipeline that decides what is allowed to merge and what is allowed to ship. When code comes from coding agents as well as people, that pipeline is the only reviewer that reads every line: agents reproduce idioms from older training data (`interface{}`, `wg.Add`/`Done`, `v := v`), drop errors with `_`, and silence a linter with a bare `//nolint` to get green. The decision is which checks gate a merge, how each one is made to actually fail, how the tools are pinned, and how the release artifact is built and scanned. The common failure is not a missing tool but a tool that runs and can never fail: `gofmt -l` without a check on its output, `govulncheck -format sarif` as the gate, `continue-on-error: true` on lint. This skill fixes the minimum pipeline. Which Go version CI installs and how `GOTOOLCHAIN` is set belong to the `go-version-policy` skill.

## The rule

1. Gate every push and pull request on `gofmt -l`, `go vet ./...`, `go fix -diff ./...` and `go test -race ./...`, failing on any output from `gofmt -l` and `go fix -diff` because both exit 0 when they find something.
2. Gate on `govulncheck ./...` in its default text mode, and use `-format json`, `sarif` or `openvex` only for a separate upload step, because those formats always exit 0.
3. Scan the artifact you ship with `govulncheck -mode binary`, after building it with the release command.
4. Lint with golangci-lint v2 and a `version: "2"` config that enables `govet`, `staticcheck`, `errcheck`, `gosec` and `bodyclose`, plus `nolintlint`.
5. Pin every CI tool to an exact version, govulncheck through a `tool` directive and golangci-lint through an exact binary version or a `tool` directive in its own modfile, and never run `go install ...@latest`.
6. Suppress a finding only by naming the rule and the reason (`#nosec G404 -- reason`, `//nolint:gosec // reason`), and configure the linters to reject anything less.
7. Commit `go.sum`, check it with `go mod tidy -diff`, leave `GOSUMDB` and `GOPROXY` at their defaults, and set `GOPRIVATE` to the narrowest pattern that covers your private modules.
8. Build release artifacts with `CGO_ENABLED=0 go build -trimpath` and leave VCS stamping on, never `-buildvcs=false`.

## Why

**Rule 1.** `gofmt -l` prints the names of unformatted files and exits 0, so a step that only runs it is green forever; fail when the output is not empty. `go vet` catches printf mismatches, copied locks, misused `errors.As` and similar bugs that compile. `go test` runs only a small subset of vet. Since Go 1.26 `go fix` is the home of the modernizers: it is built on the same analysis framework as `go vet` and rewrites code to current idioms (`any`, `wg.Go`, `min`/`max`, `strings.Cut`, `slices.Contains`, `t.Context()`, removing redundant `v := v`). The fixers are meant not to change behavior, which makes `go fix -diff` safe as a gate: any diff means the code uses an idiom the floor already replaced. In Go 1.26.0, `go fix -diff` prints the patch and still exits 0, so the gate checks for an empty diff. The race detector finds data races only on code paths a test executes, and only when tests are built with `-race`; without it a race passes every run until production. `-race` needs cgo, which GitHub's Ubuntu runners have. Run the same commands in the coding agent's post-edit hook or a pre-commit hook, so the agent sees the failure before it opens the pull request, not after.

**Rule 2.** The govulncheck documentation says it exits 0 when there are no vulnerabilities and non-zero when there are, but also exits 0 with `-json`, `-format sarif` or `-format openvex`, regardless of how many it found. A pipeline whose only govulncheck step writes SARIF for code scanning can never fail. Keep the text-mode run as the gate; if you upload SARIF, run it as a second step that is allowed to succeed. Text mode reports only vulnerabilities whose vulnerable symbols your code can reach, which is what keeps the gate low-noise; imported-but-unreachable findings are listed as informational and do not fail the build.

**Rule 3.** Source mode scans what `go.mod` says and the toolchain on the runner. The binary records the Go version and module versions it was actually built with (`go version -m` shows them), so scanning it checks the thing you ship, including a stdlib that is older than you thought. Binary mode has no call graph, so it is coarser than source mode; it complements the source gate rather than replacing it.

**Rule 4.** golangci-lint v2 does not load a v1 file: a config without `version: "2"` fails with "unsupported version of the configuration", and `golangci-lint migrate` converts it. The `standard` default set is `errcheck`, `govet`, `ineffassign`, `staticcheck` and `unused`. Add `gosec` for the security rules other skills in this repository depend on (G112/G114 server timeouts, G404 `math/rand`, G402 TLS, G107 tainted URLs), and `bodyclose` for response bodies that are never closed, which leak connections. `errcheck` with `check-blank` catches `v, _ := f()`, the most common way generated code drops an error. Turn on all `govet` analyzers except `fieldalignment`, which is about memory layout, not correctness. gosec G601 (range aliasing) applies only to Go 1.21 and lower; on the 1.26 floor never "fix" a loop with `v := v`.

**Rule 5.** `@latest` resolves at run time, so the same commit lints and scans differently next week, and nothing records which version produced a green build. A `tool` directive (Go 1.24) pins the version in a go.mod and the checksums in its go.sum, and `go tool <name>` runs it from the build cache. Keep CI tools in a separate modfile so they do not join the production module's dependency graph:

```text
go get -tool -modfile=tools.mod golang.org/x/vuln/cmd/govulncheck@v1.9.0
go tool -modfile=tools.mod govulncheck ./...
```

govulncheck v1.9.0 adds six modules, all under `golang.org/x`. golangci-lint is different: v2.15.0 as a tool pulls 211 modules (a 926-line go.sum), and its maintainers recommend the prebuilt binary over `go install`, `go get -tool` or `go tool`, because a source build uses your local Go and its dependencies can interact with your other tools. Pin it with `version: v2.15.0` in `golangci/golangci-lint-action@v9`, or, if you need it under `go tool`, give it a modfile of its own (`-modfile=golangci-lint.mod`). That is why the good example keeps `go.mod` dependency-free and puts only govulncheck in `tools.mod`. Pin the Actions themselves to a major version or a commit SHA, never a branch.

**Rule 6.** A bare `//nolint` silences every linter on the line, including checks that do not exist yet. A bare `#nosec` does the same for every gosec rule. Both turn one reviewed exception into a permanent blind spot, and agents add them to get a build green. gosec's documented format is `#nosec G404 -- justification`; in golangci-lint v2.15.0, `nosec-require-rules: enabled` and `nosec-require-justification: enabled` under `linters.settings.gosec.config.global` make a bare `#nosec` or one without `-- reason` count as a finding again. `nolintlint` with `require-specific` and `require-explanation` does the same for `//nolint`. Suppressing a rule globally (`issues.exclude-rules` with `text: ".*"`) is the config-file version of the same mistake; exclude one rule for one path, as in the good config.

**Rule 7.** `go.sum` holds the expected hash of every module; without it committed, each CI run trusts whatever the proxy serves that day. Since Go 1.16 builds run as if `-mod=readonly`, so a missing or stale entry fails the build; `GOFLAGS=-mod=mod` turns that back into a silent rewrite. `go mod tidy -diff` (Go 1.23) exits non-zero when `go.mod` or `go.sum` differ from what the code needs. `GOSUMDB=off` stops checking downloads against the public checksum database, so a compromised proxy or VCS host goes unnoticed, and toolchain downloads fail outright because they cannot be verified. `GOPRIVATE` is a list of glob patterns of module path prefixes that skip both the proxy and the checksum database: `GOPRIVATE=*` or `GOPRIVATE=github.com` turns verification off for public modules too. Use `GOPRIVATE=github.com/your-org/*` or your private host, and fetch with credentials through `GOAUTH` (Go 1.24) or `.netrc`.

**Rule 8.** For Go programs without cgo, a reproducible build is `CGO_ENABLED=0 go build -trimpath`: `-trimpath` replaces the runner's absolute paths with module paths, so two builds of the same commit with the same toolchain are identical and no home directory leaks into the binary. `CGO_ENABLED=0` also gives a static binary that runs on `scratch` or distroless images. VCS stamping is on by default (`-buildvcs=auto`) and, since Go 1.24, also sets the main module's version from the tag or commit, with `+dirty` for uncommitted changes. That is what `go version -m`, `debug.ReadBuildInfo` and govulncheck binary mode read; `-buildvcs=false` erases it, and then nobody can tell which commit is running.

The cost: this pipeline is slower than `go build && go test`, `-race` makes tests several times slower, and a new modernizer in a Go release can fail a build that passed yesterday. Accept it. The fix is one `go fix ./...` commit, and a check that never fails is not a check.

## Good example

See [`examples/good/`](examples/good/).

```yaml
      - name: go fix modernizers (prints a diff, exits 0, so fail on output)
        run: |
          go fix -diff ./... > "$RUNNER_TEMP/fix.diff"
          cat "$RUNNER_TEMP/fix.diff"
          test ! -s "$RUNNER_TEMP/fix.diff"
      - name: tests with the race detector
        run: go test -race ./...
      - name: govulncheck gate (text mode exits non-zero on reachable vulns)
        run: go tool -modfile=tools.mod govulncheck ./...
      - name: golangci-lint
        uses: golangci/golangci-lint-action@v9
        with:
          version: v2.15.0
```

Every step either exits non-zero on its own or is made to fail on output, and every tool has an exact version. The release job builds with `CGO_ENABLED=0 go build -trimpath` and scans that binary with `govulncheck -mode binary`. `.golangci.yml` is a v2 config that rejects bare `#nosec` and `//nolint`, and the Go program prints the revision VCS stamping embedded in it, with a table test for the stamped, dirty and `-buildvcs=false` cases.

## Bad example

See [`examples/bad/`](examples/bad/).

```yaml
env:
  GOSUMDB: "off" # MISTAKE: disables checksum verification for every module, so a tampered proxy or VCS host goes unnoticed; toolchain downloads also fail.
  GOPRIVATE: "*" # MISTAKE: marks every module private, which also skips the proxy and the checksum database for public ones.
  GOFLAGS: -mod=mod # MISTAKE: lets go build rewrite go.mod and go.sum in CI instead of failing on an uncommitted change.
```

```yaml
      - name: govulncheck
        run: |
          go install golang.org/x/vuln/cmd/govulncheck@latest # MISTAKE: unpinned tool, not recorded in any go.mod or go.sum.
          govulncheck -format sarif ./... > govulncheck.sarif # MISTAKE: sarif (like json and openvex) always exits 0, so this gate never fails.
```

The rest of `ci.yml` runs `gofmt -l` without checking its output, tests without `-race`, installs golangci-lint `@latest` with `continue-on-error: true`, and builds the release with `-buildvcs=false` and no `-trimpath`. `.golangci.yml` is a v1 file that v2 refuses to load, with gosec excluded wholesale. `main.go` holds what that pipeline lets through: `interface{}`, `v := v` and hand-written `wg.Add`/`Done` that `go fix -diff` rewrites, a data race only `-race` reports, a bare `#nosec` over `math/rand` and a blanket `//nolint` over a dropped `http.Get` error. It compiles, and every one of these passes the bad pipeline.

## Checklist

When reviewing a pipeline under this skill, confirm:

- [ ] CI runs `gofmt -l`, `go vet ./...`, `go fix -diff ./...` and `go test -race ./...`, and fails when `gofmt -l` or `go fix -diff` prints anything.
- [ ] The govulncheck step that gates the build runs in text mode; any `-json`, `-format sarif` or `-format openvex` run is a separate upload step.
- [ ] The release job scans the built artifact with `govulncheck -mode binary`.
- [ ] `.golangci.yml` starts with `version: "2"` and enables `govet`, `staticcheck`, `errcheck`, `gosec`, `bodyclose` and `nolintlint`.
- [ ] No step runs `go install ...@latest`; govulncheck is a `tool` directive and golangci-lint has an exact version.
- [ ] Every `#nosec` names a rule and a `-- reason`, every `//nolint` names a linter and a `// reason`, and the config enforces both.
- [ ] `go.sum` is committed, `go mod tidy -diff` runs in CI, and no workflow sets `GOSUMDB=off`, `GOFLAGS=-mod=mod`, `GOINSECURE` or a `GOPRIVATE` broader than your own module paths.
- [ ] Release builds use `CGO_ENABLED=0 go build -trimpath` and do not pass `-buildvcs=false`.
- [ ] No check runs with `continue-on-error: true` or `|| true`.

## When not to apply

- **cgo is required.** A binary that links SQLite through cgo, a C library or a platform API cannot build with `CGO_ENABLED=0`. Keep `-trimpath`, pin the C toolchain in the build image, and accept that reproducibility now depends on that image too.
- **No cgo on the runner.** `-race` needs cgo and is supported only on some platforms (on Windows it also needs a C compiler). Run `-race` on one Linux job and plain `go test` on the others, rather than dropping it everywhere.
- **A fresh Go release adds modernizers.** The first CI run on a new toolchain may fail `go fix -diff` on code nobody touched. Land a separate `go fix ./...` commit; if a fixer changes behavior, report it upstream and disable that one analyzer with `-<name>=false`, never the whole step.
- **An unreachable or unfixable vulnerability.** govulncheck has no suppression mechanism (go.dev/issue/61211). When a reachable finding has no fixed version yet, gate on a reviewed allow-list of OSV IDs parsed from `-json` output, with an expiry date, instead of disabling the step.
- **Libraries.** A library has no release binary, so rule 3 and rule 8 do not apply; keep the source gates.
- **A private module proxy.** With an internal proxy (Athens, Artifactory) set `GOPROXY` to it and `GONOPROXY=none` as `go help private` shows, and keep `GOSUMDB` on for public modules.
- **Tiny scripts and throwaway prototypes.** A one-file tool with no dependencies can skip golangci-lint and the binary scan; keep `gofmt`, `go vet`, `go test -race` and `govulncheck ./...`, which take seconds.
