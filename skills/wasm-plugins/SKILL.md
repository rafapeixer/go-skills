---
name: wasm-plugins
description: Use when letting customers or third parties extend a Go service with their own code (plugins, user-defined functions, rules, filters, webhooks run in-process), when choosing between the plugin package, subprocesses and WebAssembly, when building a Go guest for GOOS=wasip1, or when reviewing a wazero host. Covers wazero instead of plugin.Open or exec, wasip1 reactors with //go:wasmexport and -buildmode=c-shared, explicit capabilities and os.Root mounts, CPU limits through context cancellation, memory page limits, compiling once and instantiating per call, and when TinyGo is worth it.
metadata:
  verified: 2026-10-17
---

# WebAssembly plugins

## The decision

Sooner or later a service has to run code it did not write: a customer's pricing rule, a partner's transform, a filter uploaded through an admin page. The decision is how that code gets into the process and what it can touch once it is there. The two answers that come to mind first are both wrong for code you do not trust: the standard `plugin` package loads a shared object into your address space with full access to memory, files, network and secrets, and running the third party's binary as a subprocess hands it everything the service user can do and a process tree to clean up. This skill takes one position: run extensions as WebAssembly modules inside wazero, a pure-Go runtime with no cgo, grant each instance only what it needs, bound its CPU time and memory, and compile each module once. Serving Go programs as WASI 0.2/0.3 components is out of scope: mainline Go has no `wasip2` port yet ([golang/go#65333](https://go.dev/issue/65333) is open, in Backlog).

## The rule

1. Run third-party extensions as WebAssembly modules in `github.com/tetratelabs/wazero`, never with `plugin.Open` and never by executing a binary the third party supplied.
2. Build Go guests as WASI reactors with `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared` and `//go:wasmexport` (Go 1.24+), and have the host run `_initialize` through `WithStartFunctions("_initialize")` before calling any export.
3. Grant each instance only the capabilities it needs, starting from wazero's empty default (no files, no environment, fake clocks, deterministic randomness), and expose anything more as narrow host functions instead of files or environment variables.
4. Mount files only as `WithFSMount(root.FS(), guestPath)` over an `os.Root` scoped to the tenant, never with `WithDirMount` or `WithReadOnlyDirMount`, and never the host's `/`.
5. Create the runtime with `WithCloseOnContextDone(true)` and `WithMemoryLimitPages`, and instantiate and call every module under a context with a deadline set by `context.WithTimeoutCause`.
6. Compile each module once with `CompileModule`, instantiate it per call or per tenant, never call one instance from two goroutines at once, and close every instance.
7. Build guests with TinyGo only when module size or instantiation time is a measured problem, and test the guest under TinyGo itself.

## Why

**Rule 1.** The `plugin` docs list the costs: plugins work only on Linux, FreeBSD and macOS; "runtime crashes are likely to occur" unless the host and every plugin are built with exactly the same toolchain, build tags and flags, which in practice means the same dependency versions too, so a third party cannot ship one independently; "a plugin is only initialized once, and cannot be closed", so upgrading a plugin means restarting the process; and plugins are poorly supported by the race detector. Above all, a plugin is ordinary Go code in your process: it reads your memory, your files and your environment, and a panic or an infinite loop in it takes the service down. A subprocess fixes the crash but not the access: it runs as the service user with the network, the filesystem and every inherited variable, unless you rebuild a sandbox with namespaces and seccomp, per OS. A Wasm module can only touch its own linear memory and the imports the host gives it. wazero implements the Wasm Core 1.0 and 2.0 specs in pure Go with no dependencies outside `golang.org/x/sys`, so the host keeps `CGO_ENABLED=0`, cross-compilation and static binaries, and guests can be written in any language that targets WASI preview 1. Use v1.12.0 or later: it supports the two latest Go releases, and its floor is Go 1.25.

**Rule 2.** Without `-buildmode=c-shared`, a `wasip1` build is a command: its `_start` runs `main` and the module exits, so there is nothing left to call. With it (Go 1.24+), the linker emits `_initialize` instead, which sets up the Go runtime and package variables but does not run `main`; `main` must still exist and stays empty. `//go:wasmexport name` exports a function to the host, with 32- and 64-bit integers and floats, `bool`, `uintptr` and `unsafe.Pointer` as parameter and result types; larger values cross through linear memory. wazero's default start function is `_start`, and a missing start function is skipped silently, so a host that forgets `WithStartFunctions("_initialize")` calls exports on an uninitialized Go runtime. Wasm is single-threaded: a goroutine the guest starts stops running when the export returns, until the host calls in again.

**Rule 3.** wazero's `ModuleConfig` starts empty: no arguments, no environment, stdin at EOF, stdout and stderr discarded, no filesystem (`path_open` fails), a wall and monotonic clock that advance 1 ms per read, sleep that returns immediately and a deterministic random source. WASI preview 1 has no call to open a network connection. That default is the sandbox; each `With...` call removes part of it. Add `WithRandSource(rand.Reader)` only if the guest generates secrets, `WithSysWalltime` only if it needs real time, and stdout only if you cap and attribute it. Never copy host environment variables into the guest. When a plugin needs data or an action (look up a price, send an event), give it a host function built with `Runtime.NewHostModuleBuilder` that does exactly that, checks its arguments and enforces the tenant's permissions, rather than a credential or a directory.

**Rule 4.** wazero's `WithDirMount` and `WithReadOnlyDirMount` follow symlinks on the host, and their docs say a guest may escape them with relative lookups. In this repository's test, a tenant directory mounted with `WithReadOnlyDirMount` (or with `WithFSMount(os.DirFS(dir))`) let the guest read a file outside it through a planted symlink; the same directory mounted as `WithFSMount(root.FS(), "/data")` returned an error. `os.Root` resolves every path inside its directory and refuses symlinks that leave it, and an `fs.FS` has no write methods, so the mount is also read-only. If a plugin must write, give it a host function that writes through the `os.Root`, not a writable mount. Mounting `/` (common in quick-start code to "make files work") exposes every key and config the service user can read. Opening untrusted paths on the host side follows the `secure-defaults` skill.

**Rule 5.** Without `WithCloseOnContextDone(true)`, wazero does not interrupt running guest code: a guest stuck in `for {}` blocks its goroutine, and possibly its OS thread, forever, whatever the context says. With it, the compiler and interpreter insert periodic checks, and when the context passed to `Call` is done, the module is closed and the call returns a `*sys.ExitError` with `sys.ExitCodeDeadlineExceeded` (or `ExitCodeContextCanceled`). It is off by default because the checks cost some speed; for untrusted code that cost is the point. Pass the same deadline to `InstantiateModule`, which runs `_initialize`, since a hostile guest can loop there too. `WithTimeoutCause` names the budget, so the error says the plugin ran out of time and not some other dependency; the context rules themselves are in `context-and-deadlines`. Memory is bounded separately: the default limit is 65536 pages (4 GiB) per instance, and `WithMemoryLimitPages(1024)` caps it at 64 MiB, so a guest that allocates without end fails its own `memory.grow` instead of driving the host into the OOM killer. Size the limit from a measurement: a trivial Go guest already ships its runtime and garbage collector.

**Rule 6.** `CompileModule` validates the binary and, with the default compiler, translates it to machine code; that is the expensive step, and `Runtime.Instantiate` (or `InstantiateWithConfig` on raw bytes) repeats it on every call. A `CompiledModule` is reusable: instantiate it per call (simplest and fully isolated, at the cost of re-running `_initialize`) or keep one instance per tenant behind a mutex when start-up time matters. wazero's docs say `Function.Call` is not goroutine-safe, and a Go guest's runtime is not reentrant, so two goroutines calling one instance corrupt it. `WithName("")` makes each instance anonymous; with a fixed name, the second instantiation in the same runtime fails. Close each instance when the call ends, or its memory stays allocated in the runtime. To avoid recompiling on every restart, add `WithCompilationCache(wazero.NewCompilationCacheWithDir(dir))` to the runtime config, and keep that directory away from anyone else, as the docs require. Treat what comes back as untrusted input: check that the export exists, validate result ranges, and use the `ok` result of `Memory().Read` before trusting a pointer and length the guest returned.

**Rule 7.** The guest in this skill's example is about 2.4 MB with Go 1.26, because it carries the full Go runtime; TinyGo builds the same source (`tinygo build -target=wasip1 -buildmode=c-shared`) into a much smaller module that instantiates faster. The price is a different compiler with partial support for reflection and parts of the standard library, so libraries that lean on reflection may fail to build or behave differently. Start with the standard toolchain, which runs the same code and tests as the rest of the service, and switch only when you have measured that download size, cold start or per-call `_initialize` time is the bottleneck.

The cost: a Wasm boundary is narrower than a Go function call. Arguments are numbers, anything else is copied through linear memory with an encoding both sides agree on, and each call through a fresh instance costs an instantiation. Frameworks such as Extism (whose Go SDK runs on wazero) handle the memory protocol for you; whatever you choose, keep the rules above on the host side.

## Good example

See [`examples/good/`](examples/good/).

```go
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(maxPages)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
```

```go
	ctx, cancel := context.WithTimeoutCause(ctx, callTimeout, errPluginBudget)
	defer cancel()

	cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize")
	if data != nil {
		cfg = cfg.WithFSConfig(wazero.NewFSConfig().WithFSMount(data, "/data"))
	}
	mod, err := h.rt.InstantiateModule(ctx, h.plugin, cfg)
	if err != nil {
		return nil, fmt.Errorf("instantiate plugin: %w", err)
	}
	defer mod.Close(context.WithoutCancel(ctx))
```

The runtime interrupts guests when their context ends and caps their memory; `NewHost` compiles the module once, and each `Call` gets a fresh, anonymous, initialized instance with a 200 ms budget and, at most, one tenant's `os.Root` mounted read-only. The guest in `testdata/guest/` (built only under `//go:build wasip1`, so `./...` on the host skips it) exports `add`, an infinite loop, a file read and an allocator. The test builds it with the same toolchain into `t.TempDir()`, so no `.wasm` is committed, and shows the loop stopped with `ExitCodeDeadlineExceeded` and the named cause, a 128 MiB allocation refused under the 64 MiB limit, no file visible without a mount, a symlink planted in the tenant directory refused, and eight concurrent calls sharing one `CompiledModule`.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
	// MISTAKE: no WithCloseOnContextDone, so a guest stuck in a loop holds its
	// goroutine forever, and no WithMemoryLimitPages, so it may grow to 4 GiB.
	rt := wazero.NewRuntime(ctx)
```

```go
		// MISTAKE: mounts the host root read-write: the plugin can read keys
		// and configs and overwrite any file the service user can write.
		WithFSConfig(wazero.NewFSConfig().WithDirMount("/", "/")).
		// MISTAKE: copies a host secret into the guest's environment; wazero
		// grants no environment variables unless you add them.
		WithEnv("DATABASE_URL", os.Getenv("DATABASE_URL"))

	// MISTAKE: Runtime.Instantiate compiles the module on every request, which
	// costs far more than the call itself; CompileModule once at load.
	mod, err := h.rt.InstantiateWithConfig(ctx, h.wasm, cfg)
```

The file also loads native extensions with `plugin.Open`, which compiles on every platform and fails at run time on Windows, calls the guest on `context.Background()` with no deadline, and never closes the instance. Each of these is what a quick-start or a "make it work" change produces: the plugin runs, the demo passes, and the first hostile or buggy module owns the process.

## Checklist

When reviewing code under this skill, confirm:

- [ ] No production code imports `plugin` or executes a binary supplied by a tenant or third party.
- [ ] Go guests are built with `GOOS=wasip1 GOARCH=wasm` and `-buildmode=c-shared`, export functions with `//go:wasmexport`, and the host sets `WithStartFunctions("_initialize")`.
- [ ] The runtime config sets `WithCloseOnContextDone(true)` and a `WithMemoryLimitPages` value sized from a measurement.
- [ ] Every `InstantiateModule` and `Call` runs under a context with a deadline, and timeout errors carry a named cause.
- [ ] `ModuleConfig` adds no host environment variables, and every clock, random source, stdout or host function it adds has a reason next to it.
- [ ] Any filesystem mount is `WithFSMount` over an `os.Root` scoped to one tenant; there is no `WithDirMount`, `WithReadOnlyDirMount` or mount of `/`.
- [ ] Modules are compiled once with `CompileModule` and instantiated from the `CompiledModule`; there is no `Instantiate` or `InstantiateWithConfig` on raw bytes in a request path.
- [ ] No module instance is called from two goroutines at once, and every instance is closed.
- [ ] Results and memory read back from a guest are validated before use.

## When not to apply

- **Extensions you build and ship yourself.** If every "plugin" is first-party code released with the service, compile it in: a registry of Go implementations behind an interface, selected by config. Wasm adds a boundary you do not need.
- **Trusted, heavy integrations.** A partner integration that needs threads, sockets or GPUs does not fit WASI preview 1. Run it as a separate service behind an API with its own credentials, and apply the `outbound-http-client` skill to the calls, rather than embedding it.
- **Throughput-critical inner loops.** When the extension runs per byte or per row on a hot path and is trusted, a Go interface call costs nanoseconds and a Wasm call plus a memory copy costs much more; measure before moving it behind a Wasm boundary, and batch the data per call if you do.
- **Your own CLI tooling.** A developer tool that runs scripts its own user wrote can exec them; the user already has those permissions. The sandbox matters when the code and the process belong to different parties.
- **Per-tenant warm instances.** When `_initialize` dominates the call, keeping one instance per tenant (serialized by a mutex, recycled after N calls or on error) bends "instantiate per call"; never share such an instance across tenants.
- **Components and `wasip2`.** If you must host WASI 0.2/0.3 components, wazero does not run them; that needs a component-model runtime such as Wasmtime through cgo, which gives up the no-cgo benefit. Revisit Go guests as components when golang/go#65333 lands.
