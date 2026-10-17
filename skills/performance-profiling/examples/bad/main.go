// Command bad shows the profiling and pooling habits that leak internals,
// fill disks or make memory worse while looking like optimizations.
package main

import (
	"bytes"
	"log"
	"net/http"
	// MISTAKE: the blank import registers /debug/pprof/* on http.DefaultServeMux,
	// and ListenAndServe(":8080", nil) below serves that mux to the internet.
	// Anyone can download heap dumps and command lines, and start CPU profiles
	// and traces in a loop (gosec G108).
	_ "net/http/pprof"
	"os"
	"runtime/trace"
	"strconv"
	"sync"
)

// MISTAKE: the pool stores []byte values. Each Put boxes the slice header into
// an interface, an allocation on every call (staticcheck SA6002); store a pointer.
var slicePool = sync.Pool{New: func() any { return make([]byte, 0, 1024) }}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func handleReport(w http.ResponseWriter, r *http.Request) {
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	for i := range rows {
		b.WriteString("row " + strconv.Itoa(i) + "\n")
	}
	w.Write(b.Bytes())
	// MISTAKE: no cap before Put. One request for a million rows grows the buffer
	// to megabytes, and the pool hands that buffer to every later 1 KB request,
	// so the heap stays inflated long after the spike (golang/go#23199).
	bufPool.Put(b)
}

func handleEcho(w http.ResponseWriter, r *http.Request) {
	buf := slicePool.Get().([]byte)[:0]
	buf = append(buf, r.URL.Query().Get("msg")...)
	w.Write(buf)
	slicePool.Put(buf)
}

func main() {
	// MISTAKE: a full execution trace from boot to exit, "so it is there when
	// we need it". A busy service writes about 10 MB per second until the disk
	// is full; a FlightRecorder keeps only the last seconds in memory.
	f, err := os.Create("always.trace")
	if err != nil {
		log.Fatal(err)
	}
	if err := trace.Start(f); err != nil {
		log.Fatal(err)
	}
	defer trace.Stop()

	http.HandleFunc("/report", handleReport)
	http.HandleFunc("/echo", handleEcho)
	// MISTAKE: nil handler means DefaultServeMux, pprof included, on the public port.
	log.Fatal(http.ListenAndServe(":8080", nil))
}
