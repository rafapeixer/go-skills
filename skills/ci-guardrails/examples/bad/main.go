// Command fetcher shows code that passes `go build` and a plain `go test ./...`,
// and only reaches main because the pipeline in ci.yml never looks for these problems.
package main

import (
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sync"
)

// MISTAKE: interface{} is a pre-1.18 idiom; `go fix -diff` rewrites it to any, but the pipeline never runs it.
func logAll(values ...interface{}) {
	for _, v := range values {
		v := v // MISTAKE: per-iteration loop variables exist since Go 1.22; go fix (forvar) removes this copy.
		fmt.Println(v)
	}
}

func token() string {
	return fmt.Sprint(rand.Int63()) // #nosec // MISTAKE: bare #nosec names no rule and no reason, hiding G404 and any rule added later.
}

func status(url string) int {
	resp, _ := http.Get(url) //nolint // MISTAKE: blanket nolint silences every linter on this line: errcheck on the dropped error, gosec G107, and any check added later.
	return resp.StatusCode
}

func countOK(urls []string) int {
	var wg sync.WaitGroup
	ok := 0
	for _, u := range urls {
		wg.Add(1) // MISTAKE: Add/Done by hand instead of wg.Go; go fix (waitgroup) rewrites it.
		go func() {
			defer wg.Done()
			if status(u) == http.StatusOK {
				ok++ // MISTAKE: data race on ok; only `go test -race` reports it, and ci.yml runs tests without -race.
			}
		}()
	}
	wg.Wait()
	return ok
}

func main() {
	logAll("token", token())
	fmt.Println(countOK(os.Args[1:]))
}
