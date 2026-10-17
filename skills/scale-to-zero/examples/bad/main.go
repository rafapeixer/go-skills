// Command bad is a long-running service ported to a scale-to-zero platform
// unchanged: every dependency is built eagerly, before main, on every cold start.
package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// MISTAKE: a package-level var that does work: all 200 report templates are
// parsed on every cold start, before main runs, though only /report uses them.
var reports = parseAllReports()

func parseAllReports() *template.Template {
	t := template.New("reports")
	for i := range 200 {
		template.Must(t.New(fmt.Sprintf("r%d", i)).Parse(`<h1>{{.Title}}</h1>{{range .Rows}}<p>{{.}}</p>{{end}}`))
	}
	return t
}

var (
	config map[string]string
	pool   []net.Conn
)

func init() {
	// Guard for this repository only: CI builds this package and compiles its
	// tests, and a test binary runs init too. The real mistake has no guard.
	if os.Getenv("RUN_BAD_EXAMPLE") == "" {
		return
	}
	// MISTAKE: network I/O in init, before the process listens. On Cloud Run the
	// request that woke the instance waits for it; on Lambda it is billed INIT time.
	resp, err := http.Get("http://config.internal/service.json")
	if err != nil {
		// MISTAKE: a blip in the config service crash-loops every new instance.
		log.Fatal(err)
	}
	defer resp.Body.Close()
	json.NewDecoder(resp.Body).Decode(&config)

	// MISTAKE: an eager pool of 10 connections with a 30s dial timeout; every
	// instance that scales up opens 10 connections whether or not traffic needs them,
	// and an unreachable database stalls startup past the platform's start limit.
	for range 10 {
		c, err := net.DialTimeout("tcp", config["db_addr"], 30*time.Second)
		if err != nil {
			log.Fatal(err)
		}
		pool = append(pool, c)
	}
}

func main() {
	stop := make(chan os.Signal, 1)
	// MISTAKE: SIGTERM only; Fly.io sends SIGINT by default, so this handler never
	// runs there and the process is killed when kill_timeout expires.
	signal.Notify(stop, syscall.SIGTERM)
	go func() {
		<-stop
		// MISTAKE: exits at once; in-flight requests get connection resets and
		// the database connections are dropped without being closed.
		os.Exit(0)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /report", func(w http.ResponseWriter, r *http.Request) {
		reports.ExecuteTemplate(w, "r0", map[string]any{"Title": "daily"})
	})
	srv := &http.Server{
		Addr:              ":" + os.Getenv("PORT"),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
