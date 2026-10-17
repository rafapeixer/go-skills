// Command server shows the container tuning habits that predate Go 1.25 and
// Go 1.19 and now cost CPU, memory or uptime.
package main

import (
	"fmt"
	"log"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"
)

// MISTAKE: a heap ballast "to make the GC run less often". GOMEMLIMIT replaced
// this trick in Go 1.19; the ballast is invisible in profiles, confuses heap
// dashboards and still has to be tuned by hand for every memory size.
var ballast = make([]byte, 256<<20)

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

func main() {
	// MISTAKE: relies on the image for the zone database. On alpine (no tzdata)
	// or scratch this fails at startup; importing time/tzdata would not.
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		log.Fatal(err)
	}

	// MISTAKE: the only memory signal is HeapAlloc from ReadMemStats, which stops
	// the world on every scrape and says nothing about GC CPU, the heap goal or
	// whether the GC CPU limiter has kicked in.
	http.HandleFunc("/debug/mem", func(w http.ResponseWriter, _ *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		fmt.Fprintf(w, "heap_alloc %d\nnow %s\n", m.HeapAlloc, time.Now().In(loc))
	})

	runtime.KeepAlive(ballast)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
