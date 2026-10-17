// Command pricing is the mock pricing service used by the benchmark.
//
//	GET /prices/{sku}  200 {"sku":..., "cents":...}
//
// SKUs starting with "X" are unknown (404). SKUs starting with "SLOW" take
// SLOW_MS to answer. Every other request takes LATENCY_MS plus up to
// JITTER_MS of random delay. POST /mode?latency_ms=N changes the base
// latency at run time, to simulate the dependency degrading mid-test.
package main

import (
	"encoding/json"
	"hash/fnv"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

func main() {
	var latency atomic.Int64
	latency.Store(int64(envInt("LATENCY_MS", 20)))
	jitter := envInt("JITTER_MS", 10)
	slow := time.Duration(envInt("SLOW_MS", 30000)) * time.Millisecond

	mux := http.NewServeMux()
	mux.HandleFunc("GET /prices/{sku}", func(w http.ResponseWriter, r *http.Request) {
		sku := r.PathValue("sku")
		delay := time.Duration(latency.Load())*time.Millisecond + time.Duration(rand.IntN(jitter+1))*time.Millisecond
		if strings.HasPrefix(sku, "SLOW") {
			delay = slow
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		if strings.HasPrefix(sku, "X") {
			http.Error(w, `{"error":"unknown sku"}`, http.StatusNotFound)
			return
		}
		h := fnv.New32a()
		h.Write([]byte(sku))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"sku": sku, "cents": 100 + int(h.Sum32()%9900)})
	})
	mux.HandleFunc("POST /mode", func(w http.ResponseWriter, r *http.Request) {
		if v, err := strconv.Atoi(r.URL.Query().Get("latency_ms")); err == nil {
			latency.Store(int64(v))
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := &http.Server{Addr: ":8081", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
