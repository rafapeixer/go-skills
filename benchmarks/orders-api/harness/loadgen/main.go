// Command loadgen drives orders-api at fixed request rates (open loop) and
// prints one JSON line per phase.
//
// Requests are scheduled on a fixed clock and latency is measured from the
// scheduled time, so a server that stalls is charged for the queue it causes
// (no coordinated omission). A request that takes longer than -timeout counts
// as an error.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type result struct {
	op      string
	status  int // 0 means transport error or timeout
	latency time.Duration
}

type phase struct {
	rps int
	dur time.Duration
}

func parsePhases(s string) ([]phase, error) {
	var out []phase
	for _, p := range strings.Split(s, ",") {
		rate, dur, ok := strings.Cut(p, ":")
		if !ok {
			return nil, fmt.Errorf("phase %q: want rps:duration", p)
		}
		r, err := strconv.Atoi(rate)
		if err != nil {
			return nil, err
		}
		d, err := time.ParseDuration(dur)
		if err != nil {
			return nil, err
		}
		out = append(out, phase{r, d})
	}
	return out, nil
}

func main() {
	base := flag.String("url", "http://lb:8080", "orders-api base URL")
	phases := flag.String("phases", "100:20s", "comma-separated rps:duration")
	timeout := flag.Duration("timeout", 2*time.Second, "per-request timeout")
	maxOrder := flag.Int("max-order-id", 200000, "highest seeded order id")
	customers := flag.Int("customers", 1000, "seeded customer ids are 1..customers")
	label := flag.String("label", "", "label copied into every output line")
	flag.Parse()

	ps, err := parsePhases(*phases)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 20000
	tr.MaxIdleConnsPerHost = 20000
	client := &http.Client{Transport: tr, Timeout: *timeout}

	for _, p := range ps {
		results := run(client, *base, p, *maxOrder, *customers)
		report(*label, p, results)
	}
}

func run(client *http.Client, base string, p phase, maxOrder, customers int) []result {
	n := int(float64(p.rps) * p.dur.Seconds())
	interval := time.Second / time.Duration(p.rps)
	results := make([]result, n)
	var wg sync.WaitGroup
	start := time.Now()
	for i := range n {
		scheduled := start.Add(time.Duration(i) * interval)
		time.Sleep(time.Until(scheduled))
		wg.Go(func() {
			op, req := pick(base, maxOrder, customers)
			resp, err := client.Do(req)
			status := 0
			if err == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				status = resp.StatusCode
			}
			results[i] = result{op: op, status: status, latency: time.Since(scheduled)}
		})
	}
	wg.Wait()
	return results
}

// pick returns one request from the workload mix.
func pick(base string, maxOrder, customers int) (string, *http.Request) {
	switch x := rand.IntN(100); {
	case x < 45:
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/orders/%d", base, 1+rand.IntN(maxOrder)), nil)
		return "get_order", req
	case x < 70:
		// Customer 1 is the heavy one with tens of thousands of orders.
		c := 1 + rand.IntN(customers)
		if rand.IntN(10) == 0 {
			c = 1
		}
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/customers/%d/orders?limit=20", base, c), nil)
		return "list_orders", req
	case x < 85:
		body := fmt.Sprintf(`{"customer_id":%d,"items":[{"sku":"A%d","qty":1},{"sku":"B%d","qty":2},{"sku":"C%d","qty":3}]}`,
			1+rand.IntN(customers), rand.IntN(500), rand.IntN(500), rand.IntN(500))
		req, _ := http.NewRequest("POST", base+"/orders", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		return "create_order", req
	default:
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/orders/%d/quote", base, 1+rand.IntN(maxOrder)), nil)
		return "quote", req
	}
}

func report(label string, p phase, rs []result) {
	ok, errs := 0, map[string]int{}
	lat := make([]time.Duration, 0, len(rs))
	perOp := map[string][]time.Duration{}
	for _, r := range rs {
		lat = append(lat, r.latency)
		perOp[r.op] = append(perOp[r.op], r.latency)
		if r.status >= 200 && r.status < 300 || r.status == 404 {
			ok++
		} else {
			errs[strconv.Itoa(r.status)]++
		}
	}
	ops := map[string]map[string]float64{}
	for op, ls := range perOp {
		ops[op] = quantiles(ls)
	}
	out := map[string]any{
		"label":      label,
		"target_rps": p.rps,
		"duration_s": p.dur.Seconds(),
		"requests":   len(rs),
		"ok":         ok,
		"errors":     errs,
		"error_rate": 1 - float64(ok)/float64(max(len(rs), 1)),
		"latency_ms": quantiles(lat),
		"by_op_ms":   ops,
	}
	json.NewEncoder(os.Stdout).Encode(out)
}

func quantiles(ls []time.Duration) map[string]float64 {
	if len(ls) == 0 {
		return nil
	}
	slices.Sort(ls)
	q := func(f float64) float64 {
		return float64(ls[min(len(ls)-1, int(f*float64(len(ls))))].Microseconds()) / 1000
	}
	return map[string]float64{"p50": q(0.50), "p90": q(0.90), "p99": q(0.99), "max": q(1)}
}
