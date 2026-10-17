---
name: container-runtime-tuning
description: Use when writing a Dockerfile or Kubernetes manifest for a Go service, setting CPU or memory limits, choosing GOMAXPROCS, GOMEMLIMIT or GOGC, reviewing automaxprocs or heap ballast code, picking a base image, or alerting on Go memory and GC behavior in containers. Covers container-aware GOMAXPROCS (Go 1.25+), CPU limits vs requests, GOMEMLIMIT from the Downward API, GOGC and ballast, GC CPU from runtime/metrics, and distroless/scratch multi-arch images.
metadata:
  verified: 2026-10-17
---

# Container runtime tuning

## The decision

A Go service in a container runs with whatever the runtime infers about its CPU and memory, plus whatever knobs someone copied from an old blog post. Go 1.25 made `GOMAXPROCS` follow the cgroup CPU limit, so the old fixes (`automaxprocs`, a hard-coded `GOMAXPROCS`) now switch a good default off. The runtime still does not read the cgroup memory limit, so without `GOMEMLIMIT` the garbage collector has no idea the pod is about to be OOM-killed. Getting this wrong costs money every hour (CPU throttling, oversized pods kept "just in case") and uptime on bad days (OOM kills, GC death spirals). This skill fixes which runtime settings you set, which you leave alone, how you watch them, and what image the binary ships in. Finding hot paths with profiles and PGO is a separate decision.

## The rule

1. Remove `automaxprocs` and never set `GOMAXPROCS` (environment or `runtime.GOMAXPROCS`) once `go.mod` declares Go 1.25 or later, because an explicit value turns off the container-aware default and its updates.
2. Set a CPU limit on every Go container, because the runtime derives `GOMAXPROCS` from the cgroup CPU limit and ignores CPU requests.
3. Set a memory limit and derive `GOMEMLIMIT` from it at 90 to 95% through the Downward API, because the runtime does not read the cgroup memory limit.
4. Keep `GOGC` at its default and use no heap ballast, and set `GOGC=off` only when the process is alone in its cgroup with `GOMEMLIMIT` set and a live heap well below it.
5. Export GC CPU, heap goal and GC limiter activity from `runtime/metrics` and alert on the GC share of CPU, not only on RSS.
6. Ship a static (`CGO_ENABLED=0`) binary on `gcr.io/distroless/static-debian13:nonroot` or `scratch`, with `time/tzdata` imported in `main` and CA certificates in the image.
7. Publish one image index for `linux/amd64` and `linux/arm64`, cross-compiled from the native build platform.

## Why

**Rule 1.** Before Go 1.25, `GOMAXPROCS` defaulted to `runtime.NumCPU()`, the node's logical CPUs, so a 2-CPU pod on a 64-core node ran 64 Ps; `go.uber.org/automaxprocs` existed to fix that. Since Go 1.25, on Linux, the default is the cgroup CPU bandwidth limit when it is lower than the CPU count, and on all OSes the runtime re-checks the CPU count and the limit periodically (the scheduler's monitor checks about once per second) and adjusts `GOMAXPROCS` when they change, for example after an in-place pod resize. Both behaviors are disabled the moment `GOMAXPROCS` is set through the environment variable or `runtime.GOMAXPROCS`; `automaxprocs` calls `runtime.GOMAXPROCS` at init, so keeping it freezes the value at startup and drops the updates. The new default applies only when `go.mod` says `go 1.25.0` or later, which this repository's floor (`go 1.26.0`) already guarantees. The GODEBUG settings `containermaxprocs=0` and `updatemaxprocs=0` turn the two behaviors off explicitly; `runtime.SetDefaultGOMAXPROCS` restores the default after something overrode it. Remove the import rather than layering both.

**Rule 2.** A Kubernetes CPU limit becomes a CFS bandwidth quota (100 ms period by default), and that is what the runtime reads; a CPU request is only a scheduling guarantee and the runtime does not consider it. With requests and no limit, `GOMAXPROCS` is the node's core count again: many Ps compete for a small share, GC runs more parallel workers than the pod can feed, and every node size gives a different behavior. The runtime rounds a fractional limit up and never goes below 2 (unless the node has fewer CPUs), so `cpu: 2500m` gives 3 and `cpu: 500m` gives 2; prefer whole-CPU limits so the Ps match the quota. The cost: a limit can throttle a spiky service that would otherwise borrow idle node CPU. Throttling pauses the whole process for the rest of the 100 ms period, which shows as tail latency, so size the limit from measured peak usage rather than leaving it off.

**Rule 3.** `GOMEMLIMIT` (Go 1.19) is a soft limit on the memory the runtime manages (`/memory/classes/total:bytes` minus `/memory/classes/heap/released:bytes`): as total memory approaches it, the GC runs more often instead of letting the heap double under `GOGC=100`. Without it, a pod with a 512 MiB limit and a 300 MiB live heap can reach a 600 MiB heap goal and be OOM-killed while the GC was nowhere near busy. The GC guide recommends this exact setup for a service in a container with fixed memory and says to leave 5 to 10% headroom for memory the runtime does not track (binary text, cgo, kernel buffers). The runtime does not derive a default from the cgroup; proposal golang/go#75164 is still open. Read the limit with a `resourceFieldRef` to `limits.memory` and compute the 90% in code, so the number cannot drift from the manifest. Two details: an environment variable from the Downward API does not change on an in-place resize until the container restarts, and if the container has no memory limit the kubelet exposes the node's allocatable memory instead, so rule 3 depends on the limit being set. Set the memory request equal to the limit: memory is not compressible, and the GC guide warns that a limit is less reliable when the node is overcommitted.

**Rule 4.** `GOGC` trades CPU for memory: the heap goal is the live heap plus (live heap plus GC roots) times `GOGC/100`. With `GOMEMLIMIT` in place, the default of 100 already makes the GC work harder only when memory is tight. `GOGC=off` with a limit lets the heap grow to the limit before every collection, which is safe only when nothing else shares that memory (the GC guide says not to do it when memory is shared with decoupled processes) and the live heap is far below the limit. `GOGC=off` without a limit is a slow OOM. A heap ballast (a large unused allocation that inflates the heap goal) was the pre-1.19 way to get fewer collections; the soft memory limit design names replacing it as a goal, and a ballast still needs hand-tuning per pod size and hides real heap numbers. The real risk near the limit is thrashing: when the live heap approaches `GOMEMLIMIT`, the GC runs constantly. The runtime caps GC CPU at roughly 50% over a `2 × GOMAXPROCS` CPU-second window, so a mis-set limit slows the program by at most about 2x but memory can then exceed the limit. The guide's advice is to raise the container limit or lower `GOGC`, never to use the memory limit to dodge an OOM.

**Rule 5.** A GC death spiral is invisible on an RSS graph: memory sits flat just under the limit while latency doubles. The signal is the GC's share of CPU. `runtime/metrics` exposes `/cpu/classes/gc/total:cpu-seconds` and `/cpu/classes/total:cpu-seconds` as cumulative counters; alert on the ratio of their rates (a sustained share well above the service's baseline, and anything near 50% means the limiter is active). These CPU metrics are estimates, so compare them only with each other, never with container CPU usage. Export `/gc/heap/goal:bytes` and `/gc/gomemlimit:bytes` to see how close the goal runs to the limit, and `/gc/limiter/last-enabled:gc-cycle`, which becomes non-zero once the CPU limiter has engaged. Read them with `metrics.Read` rather than `runtime.ReadMemStats`, which stops the world on every call. Names can change between releases and an unknown name silently returns `KindBad`, so a test checks every name against `metrics.All()`.

**Rule 6.** A static Go binary needs no libc, no shell and no package manager, so the image can be just the binary plus CA certificates and a zone database. `distroless/static` provides CA certificates, tzdata, `/etc/passwd` and `/tmp`, and its `nonroot` tag runs as an unprivileged user; the Debian 13 variant is the one distroless currently publishes. With `scratch` you must copy `/etc/ssl/certs/ca-certificates.crt` yourself or every outbound TLS call fails. Importing `time/tzdata` in `main` embeds the zone database (about 450 KB) as a fallback, so `time.LoadLocation` works on `scratch`, on alpine without the `tzdata` package, and after someone changes the base image. A smaller image also starts faster and has fewer CVEs to triage.

**Rule 7.** arm64 nodes are usually cheaper per unit of CPU, and an amd64-only image either cannot schedule there or fails with `exec format error`. Go cross-compiles without emulation: run the builder stage on `$BUILDPLATFORM` and set `GOOS`/`GOARCH` from `TARGETOS`/`TARGETARCH`, then `docker buildx build --platform linux/amd64,linux/arm64` publishes one tag both architectures pull.

None of this replaces upgrading Go: Go 1.26 made the Green Tea garbage collector the default, which the release notes expect to cut GC overhead by 10 to 40% in GC-heavy programs, before any knob is touched. Profiling and PGO belong to `performance-profiling`.

## Good example

See [`examples/good/`](examples/good/).

```go
func applyMemoryLimit(getenv func(string) string) (int64, error) {
	raw := getenv("MEMORY_LIMIT_BYTES")
	if getenv("GOMEMLIMIT") != "" || raw == "" {
		return debug.SetMemoryLimit(-1), nil // -1 reads the limit without changing it
	}
	limit, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("parse MEMORY_LIMIT_BYTES %q: want a positive byte count", raw)
	}
	debug.SetMemoryLimit(limit / 10 * 9)
	return debug.SetMemoryLimit(-1), nil
}
```

```yaml
          env:
            # The container memory limit in bytes. The program sets GOMEMLIMIT
            # to 90% of it. No GOMAXPROCS and no GOGC: the defaults are right.
            - name: MEMORY_LIMIT_BYTES
              valueFrom:
                resourceFieldRef:
                  containerName: api
                  resource: limits.memory
```

The manifest sets a CPU limit and a memory limit and passes only the memory limit through; the program turns it into a 90% soft limit (an explicit `GOMEMLIMIT` still wins), logs the effective `GOMAXPROCS` and `GOMEMLIMIT` at startup, imports `time/tzdata`, and serves the GC CPU counters, heap goal, memory limit and limiter cycle from `runtime/metrics` on `/debug/runtime`. The `Dockerfile` cross-compiles a static binary onto `distroless/static-debian13:nonroot` for both architectures, and a test fails if any metric name the dashboards use is not in `metrics.All()`.

## Bad example

See [`examples/bad/`](examples/bad/).

```go
func init() {
	// MISTAKE: stands in for `import _ "go.uber.org/automaxprocs"` or a hand-set
	// GOMAXPROCS. Any explicit value switches off the Go 1.25 default, so the
	// runtime stops following the cgroup CPU limit and its changes. NumCPU is the
	// node's core count: 64 Ps in a 2-CPU pod, then CFS throttling and tail latency.
	runtime.GOMAXPROCS(runtime.NumCPU())

	// MISTAKE: GOGC=off in code with no memory limit. The heap grows until the
	// kernel kills the pod; with a limit too close to the live heap it would
	// instead spend up to half the CPU on GC.
	debug.SetGCPercent(-1)
}
```

```yaml
          # MISTAKE: no resources at all. Without a CPU limit GOMAXPROCS is the
          # node's core count (64 on a 64-core node); without a memory limit the
          # node, not the pod, decides when the process is killed.
          env:
            # MISTAKE: GOGC=off with no GOMEMLIMIT and no memory limit. The heap
            # grows until the kernel OOM-kills the process, on every pod.
            - name: GOGC
              value: "off"
```

The program also keeps a 256 MiB ballast, watches only `HeapAlloc` through `ReadMemStats`, and loads a time zone from the image; the `Dockerfile` hard-codes `GOARCH=amd64` and ships on alpine without tzdata, as root. Each of these was reasonable advice for some Go release before 1.19 or 1.25, and each works on a laptop, which is why they survive in templates.

## Checklist

When reviewing code under this skill, confirm:

- [ ] `go.mod` declares Go 1.25 or later, nothing imports `go.uber.org/automaxprocs`, and neither the code nor the manifest sets `GOMAXPROCS`.
- [ ] Every Go container has a CPU limit, preferably a whole number of CPUs, sized from measured peak usage.
- [ ] Every Go container has a memory limit equal to its memory request.
- [ ] `GOMEMLIMIT` is derived from `limits.memory` (Downward API) at 90 to 95%, not hard-coded separately from the limit and not missing.
- [ ] `GOGC` is unset, or set to `off` only for a process alone in its cgroup with `GOMEMLIMIT` and a live heap well below it; there is no heap ballast.
- [ ] GC CPU share, heap goal, memory limit and GC limiter activity are exported from `runtime/metrics` and alerted on, and a test checks the metric names against `metrics.All()`.
- [ ] The image is a static binary on `distroless/static` (nonroot) or `scratch` with CA certificates, and `main` imports `time/tzdata`.
- [ ] The image is built for `linux/amd64` and `linux/arm64` by cross-compiling from `$BUILDPLATFORM`.
- [ ] Startup logs the effective `GOMAXPROCS` and `GOMEMLIMIT`, so a wrong limit is visible in the first log lines.

## When not to apply

- **CLIs, batch jobs on shared hosts and desktop tools.** Do not bake in a memory limit: the GC guide advises against it when you do not control the environment and memory scales with input. Let the user set `GOMEMLIMIT` if they need one.
- **Latency-critical services on dedicated nodes with no CPU limit by policy.** Some platforms avoid CPU limits to prevent throttling. Then set `GOMAXPROCS` explicitly to the CPU request rounded up, document why, and accept that it no longer tracks resizes.
- **Several processes in one container.** A Go binary started next to another program in the same container shares its memory limit; give the Go process a `GOMEMLIMIT` that covers only its share, keep `GOGC` on, and never use `GOGC=off`.
- **cgo binaries.** A service that needs cgo (for example a C SQLite driver or a native crypto module) cannot use `distroless/static` or `scratch`; use `gcr.io/distroless/base-debian13` and leave more than 10% headroom, because C allocations are outside `GOMEMLIMIT`.
- **A live heap already close to the memory limit.** Raise the limit first and then set `GOMEMLIMIT`; lowering the limit or `GOGC=off` only trades an OOM for a GC spiral.
- **Debugging in production.** distroless has no shell; use the `:debug` tag in a temporary image or `kubectl debug` with an ephemeral container instead of switching the base image.
