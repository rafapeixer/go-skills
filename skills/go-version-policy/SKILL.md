---
name: go-version-policy
description: Use when creating a go.mod, choosing or reviewing its go and toolchain lines, upgrading Go, reacting to a dependency that raised its go line, setting GOTOOLCHAIN in CI, pinning developer tools, or writing code or docs that need a Go release newer than the module's floor. Covers the oldest-supported-release floor (go 1.26.0 today), GOTOOLCHAIN=local in CI, what the go line changes in language semantics, runtime and GODEBUG defaults, go fix modernizers on every floor bump, the tool directive, version gating with build tags and go/version, and the six-month review cadence.
---

# Go version policy

## The decision

Every module states two versions in `go.mod`: the `go` line, the minimum release that may build it, and an optional `toolchain` line, a suggestion for which compiler to run. Most teams set them once with `go mod init` and never look again, and that hides three problems. The floor drifts below the releases that still get security fixes. The `go` line is not just a minimum: it also selects language semantics, runtime defaults and GODEBUG settings, so a service built with Go 1.27 but declaring `go 1.21` still runs with Go 1.21 behavior in places. And with the default `GOTOOLCHAIN=auto`, one `go get` of a dependency that raised its own `go` line silently raises yours and switches the compiler. This skill fixes which versions you declare, how CI picks the compiler, and how and when you move the floor. The lint, vet and vulnerability pipeline that runs on top is the `ci-guardrails` skill.

## The rule

1. Declare the oldest Go release that still receives security fixes as the `go` line, written as a full release (`go 1.26.0` today), and leave out the `toolchain` line.
2. Build and test in CI on the newest patch of both the floor and the latest release with `GOTOOLCHAIN=local`, and ship binaries built with the newest patch, never with the floor's `.0`.
3. Change the `go` line only in its own reviewed commit, because it also changes loop semantics, runtime defaults and GODEBUG defaults, not just the minimum version.
4. After every floor bump, run `go fix ./...` until it reports nothing, and commit the result separately from hand-written changes.
5. Pin developer tools with the `tool` directive (`go get -tool`) and run them with `go tool`, never with a `tools.go` file or `go install ...@latest`.
6. Gate anything that needs a release newer than the floor: mark rules "(Go 1.27+)", put such code in a file with `//go:build go1.27`, and compare versions with `go/version`, never as strings.
7. Review the floor every six months, when Go ships its February and August releases, and raise it to the release that is now the oldest supported.

## Why

**Rule 1.** Go's policy is that "each major Go release is supported until there are two newer major releases". Go 1.26 shipped on 2026-02-10 and Go 1.27 on 2026-08-19, so today only 1.26 and 1.27 get security fixes; Go 1.25 got its last patch, 1.25.14, the day 1.27 shipped. A floor of `go 1.26.0` tells every consumer the module needs a supported release and nothing newer. A lower floor promises support for compilers nobody patches; a higher one locks out people on the other supported release. Write the full release (`1.26.0`), not the language version (`1.26`): `1.26` names a language, not a release, and sorts below `1.26.0`, so the full form says exactly which release you test against. `go mod init` does not do this for you: since Go 1.26 it writes `go 1.(N-1).0`, so a 1.26 toolchain writes `go 1.25.0`, already unsupported. Follow it with `go get go@1.26.0`. Without a `toolchain` line the module behaves as if it said `toolchain go1.26.0`, which is what you want; since Go 1.25 the `go` command no longer adds one when it updates the `go` line, so a `toolchain` line you find is usually stale (`go get go@1.26.0` removes one below the new floor).

**Rule 2.** The floor says what may build the module, not what should. Patch releases carry the security fixes: go1.26.9 and go1.27.2 (2026-10-08) fixed the `go` command, `crypto/tls`, `html/template`, `net/http` and `os`. With the default `GOTOOLCHAIN=auto`, the `go` command switches to a newer toolchain whenever the `go` or `toolchain` line asks for one, and `go get` raises your `go` line to the highest `go` line among your dependencies. In CI that means a dependency bump can download and use a compiler nobody chose, and the run still goes green. `GOTOOLCHAIN=local` "always runs the bundled Go toolchain", and a toolchain "refuses to load a module ... that declares a minimum required Go version greater than the toolchain's own version", so the same bump fails loudly instead. Testing on the floor proves the module still builds for consumers on the older supported release; testing on the latest proves the next floor bump is safe. Locally, `auto` is fine: downloaded toolchains are verified against the checksum database.

**Rule 3.** The `go` line is a behavior switch. Per-iteration loop variables apply only "in packages contained in modules that declare go 1.22 or later". GOMAXPROCS follows the container CPU limit only when the `go` line is 1.25.0 or later. Tracebacks include goroutine labels for `go 1.27` modules. And for every compatibility GODEBUG, the toolchain "amends its defaults to match" the older `go` line, so a module that declares an old release runs a new compiler with old defaults. Each of these is a production change that looks like a one-character diff, which is why it gets its own commit, a changelog line and a deploy you can roll back. The reverse also holds: never lower the `go` line to make a build pass. When a dependency requires a newer floor than you are ready for, hold that dependency back.

**Rule 4.** `go fix` was rebuilt in Go 1.26 as the home of the modernizers, "dozens of fixers" that rewrite code to newer idioms without changing behavior. They only fire in files whose `go` version allows the new idiom, so the floor bump is exactly when new fixes become available. On the bad example, `go get go@1.26.0` followed by `go fix` deleted the `name := name` copies (`forvar`), turned `wg.Add`/`Done` into `wg.Go`, and replaced the pointer helper with `new(expr)` (`newexpr`). Run it from a clean tree, preview with `go fix -diff ./...`, and run it again until it reports nothing (twice is usually enough). Coding agents keep generating pre-1.22 idioms from old training data; enforcing a clean `go fix -diff` in CI is part of `ci-guardrails`.

**Rule 5.** Since Go 1.24, `tool` directives in `go.mod` track executables the way `require` tracks libraries, replacing the blank-import `tools.go` workaround. `go get -tool golang.org/x/vuln/cmd/govulncheck` records the current version as a requirement plus a `tool` line, `go tool govulncheck` runs the pinned version, and `go get tool` upgrades them all. `go install ...@latest` in a CI script gives every run whatever was released that morning, recorded in no `go.sum`; `tools.go` pins versions too, but it is a convention the `go` command does not know: it needs a build tag to stay out of the build, `go tool` cannot run what it lists, and nothing upgrades the set at once. Tools pinned in `go.mod` also make a tool's own `go` line visible: if it requires a newer release than your floor, `go get -tool` raises your `go` line, and under rule 2 CI tells you.

**Rule 6.** A rule or snippet that needs Go 1.27 (generic methods, `crypto/mldsa`, the `waitgroupgo` analyzer name) fails to compile on a 1.26 floor, and an agent following an unmarked rule writes exactly that code. Language features newer than the `go` line are compile errors; standard library symbols are not, but since Go 1.27 `go test` runs the `stdversion` vet check by default and reports them. When you need a newer feature before the floor moves, a `//go:build go1.27` file raises that file's version, with a `//go:build !go1.27` sibling as the fallback. Version strings do not sort lexically: `"go1.9" >= "go1.26.0"` is true. `go/version.Compare` (Go 1.22) orders releases correctly and sorts invalid strings, such as development builds, below valid ones.

**Rule 7.** Go ships a major release every February and August, and each one ends support for the release two back. A floor nobody reviews is a floor that is a year out of date within twelve months. Put the review on the calendar for the week after each release: raise the `go` line (rule 3), run `go fix` (rule 4), update the CI matrix (rule 2), and drop any "(Go 1.N+)" marks the new floor makes redundant (rule 6). This repository follows the same cycle: its floor moves to `go 1.27.0` when Go 1.28 ships.

The cost: a floor that moves every six months forces consumers on the oldest supported release to upgrade on the same schedule, and `GOTOOLCHAIN=local` turns some dependency updates into red builds that need a decision. Both are the point: an unsupported compiler or an unreviewed behavior change is worse than a red build.

## Good example

See [`examples/good/`](examples/good/).

```text
module github.com/rafapeixer/go-skills/skills/go-version-policy/examples/good

// The floor: the oldest Go release that still receives security fixes.
// No toolchain line: CI builds with GOTOOLCHAIN=local and the newest patch.
go 1.26.0
```

```go
func atLeast(toolchain, floor string) bool {
	if !version.IsValid(toolchain) {
		return true
	}
	return version.Compare(toolchain, floor) >= 0
}
```

```go
	for i, name := range names {
		wg.Go(func() { out[i] = "ok " + name })
	}
```

The module declares the supported floor and nothing else. The program reads its toolchain from `debug.ReadBuildInfo`, stores the VCS revision with `new(s.Value)`, and relies on per-iteration loop variables without copies. The tests check that a `floor` constant matches the `go` line of `go.mod`, that the line is a full release, that the running toolchain meets it, and that `go1.9` does not pass a check that string comparison would let through.

## Bad example

See [`examples/bad/`](examples/bad/).

```text
// MISTAKE: a stale toolchain line recorded by whoever last ran go get. It names
// a release with no security fixes, and with GOTOOLCHAIN=auto it is a second
// version knob that drifts from the one CI actually uses.
toolchain go1.21.0
```

```go
	for i, name := range names {
		// MISTAKE: the pre-1.22 copy. It is needed here only because the go line
		// is below 1.22; delete it without raising the floor and every goroutine
		// may read the last name. Raise the floor and go fix (forvar) removes it.
		i, name := i, name
		// MISTAKE: Add and Done by hand. sync.WaitGroup.Go (Go 1.25) cannot
		// forget the Add; go fix rewrites this once the floor allows it.
		wg.Add(1)
```

The `go.mod` declares `go 1.21`, the floor many templates and generated projects still carry. The code builds and passes `go vet` on Go 1.26 and 1.27, which is why nobody notices: the loop copy is load-bearing, `new(expr)` is a compile error so a `ptr` helper survives, `GOMAXPROCS` is set by hand because the container-aware default is off below `go 1.25`, and the version check compares strings.

## Checklist

When reviewing code under this skill, confirm:

- [ ] The `go` line is the oldest supported release written as a full version (`go 1.26.0` today), not a language version such as `go 1.26` and not an unsupported release.
- [ ] There is no `toolchain` line, or the commit that added it says which patch it requires and why.
- [ ] CI sets `GOTOOLCHAIN=local` and tests on the newest patch of the floor and of the latest release; release binaries use the newest patch.
- [ ] A diff that changes the `go` line is its own commit and names the behavior changes it brings (loop variables, GOMAXPROCS, GODEBUG defaults).
- [ ] The commit after a floor bump contains only `go fix` output, and `go fix -diff ./...` is now empty.
- [ ] Developer tools appear as `tool` lines in `go.mod`; no `tools.go` file and no `go install ...@latest` in scripts.
- [ ] Code using features newer than the floor sits behind `//go:build go1.N`, and docs mark such rules "(Go 1.N+)".
- [ ] Version comparisons use `go/version`, never `<` or `>=` on strings.
- [ ] No `x := x` loop copies, pointer helpers or manual `GOMAXPROCS` remain as workarounds for an old `go` line.

## When not to apply

- **Applications that need the newest release now.** A service you deploy yourself, with no importers, may declare the newest release (`go 1.27.0`) to get a feature or a `go`-line-gated default early. Never declare anything older than the oldest supported release; that part of rule 1 has no exception.
- **Distributions with long-term support toolchains.** Some enterprise Linux distributions backport security fixes into an older Go. If you must build with that toolchain, the floor is what it ships, and rule 2 becomes "the vendor's newest patch".
- **A dependency demands a floor you are not ready for.** Hold the dependency at the last version that fits your floor and open an upgrade task; do not let `go get` raise the `go` line inside an unrelated change.
- **Local development.** `GOTOOLCHAIN=auto` (the default) is fine on a laptop: downloads are checksum-verified, and it lets one machine work on modules with different floors. Rule 2 is about CI and release builds.
- **Pinning one patch for a known fix.** When the floor's `.0` has a bug or vulnerability you must avoid, a `toolchain go1.26.9` line makes local `auto` builds pick a fixed patch. Keep the `go` line at the floor, and remove the `toolchain` line at the next floor bump.
- **Throwaway scripts and examples.** A one-off program run once with the toolchain you have needs no CI matrix or tool directives; still give it a supported `go` line.
