package main

import (
	"net/http"
	"net/http/pprof"
	"time"
)

// serveDebug exposes pprof on an internal port that the load balancer does
// not route to, with handlers registered explicitly on their own mux.
func serveDebug(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	srv.ListenAndServe()
}
