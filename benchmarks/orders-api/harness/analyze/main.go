// Command analyze aggregates repeated benchmark runs into Markdown tables.
//
//	go run ./analyze ../results
//
// Runs are directories named <scenario>-<impl>-r<N>. Every metric is
// reported as the median across repetitions with the min–max range, so a
// single noisy run cannot carry a conclusion.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	sloP99Ms     = 200.0
	sloErrorRate = 0.01
)

type phase struct {
	TargetRPS int                `json:"target_rps"`
	ErrorRate float64            `json:"error_rate"`
	Latency   map[string]float64 `json:"latency_ms"`
}

type run struct {
	Contract string `json:"contract"`
	Replicas int    `json:"replicas"`
	phases   []phase
	cpuCores []float64
	memMiB   float64
	pgConns  int
}

var repSuffix = regexp.MustCompile(`-r\d+$`)

func main() {
	dir := os.Args[1]
	entries, _ := os.ReadDir(dir)
	groups := map[string][]*run{}
	for _, e := range entries {
		r, err := load(filepath.Join(dir, e.Name()))
		if err != nil {
			fmt.Fprintln(os.Stderr, "skip", e.Name(), err)
			continue
		}
		base := repSuffix.ReplaceAllString(e.Name(), "")
		groups[base] = append(groups[base], r)
	}
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	slices.Sort(names)

	fmt.Print("### Capacity: highest rate within SLO, per repetition\n\n")
	fmt.Println("| Run | Repetitions | Contract | Capacity per repetition (req/s) | Median |")
	fmt.Println("|---|---|---|---|---|")
	for _, n := range names {
		if !strings.HasPrefix(n, "s1-") && !strings.HasPrefix(n, "s5-") && !strings.HasPrefix(n, "s7-") {
			continue
		}
		var caps []float64
		contract := "passed"
		for _, r := range groups[n] {
			c := 0
			for _, p := range r.phases {
				if p.Latency["p99"] < sloP99Ms && p.ErrorRate < sloErrorRate {
					c = p.TargetRPS
				} else {
					break
				}
			}
			caps = append(caps, float64(c))
			if r.Contract != "passed" {
				contract = r.Contract
			}
		}
		fmt.Printf("| %s | %d | %s | %s | %.0f |\n", n, len(caps), contract, list(caps), median(caps))
	}

	fmt.Print("\n### CPU per request in S1 (same rate for every implementation that reaches it)\n\n")
	fmt.Println("| Run | Rate | CPU ms per request: median (min–max) |")
	fmt.Println("|---|---|---|")
	for _, n := range names {
		if !strings.HasPrefix(n, "s1-") {
			continue
		}
		for _, rate := range []int{1200, 3000} {
			var ms []float64
			for _, r := range groups[n] {
				for i, p := range r.phases {
					if p.TargetRPS == rate && p.ErrorRate < sloErrorRate && p.Latency["p99"] < sloP99Ms {
						ms = append(ms, phaseCPU(r, i)/float64(rate)*1000)
					}
				}
			}
			if len(ms) > 0 {
				fmt.Printf("| %s | %d | %s |\n", n, rate, rng(ms, "%.3f"))
			}
		}
	}

	fmt.Print("\n### Every phase: median (min–max) across repetitions\n\n")
	fmt.Println("| Run | Target RPS | p50 ms | p99 ms | Error rate % |")
	fmt.Println("|---|---|---|---|---|")
	for _, n := range names {
		rs := groups[n]
		for i := range rs[0].phases {
			var p50, p99, er []float64
			for _, r := range rs {
				if i < len(r.phases) {
					p50 = append(p50, r.phases[i].Latency["p50"])
					p99 = append(p99, r.phases[i].Latency["p99"])
					er = append(er, r.phases[i].ErrorRate*100)
				}
			}
			fmt.Printf("| %s | %d | %s | %s | %s |\n", n, rs[0].phases[i].TargetRPS, rng(p50, "%.1f"), rng(p99, "%.0f"), rng(er, "%.2f"))
		}
	}

	fmt.Print("\n### Resources: median (min–max) across repetitions\n\n")
	fmt.Println("| Run | Replicas | Peak Postgres connections | Peak memory per replica MiB | Mean app CPU under load (cores) |")
	fmt.Println("|---|---|---|---|---|")
	for _, n := range names {
		var conns, mem, cpu []float64
		for _, r := range groups[n] {
			conns = append(conns, float64(r.pgConns))
			mem = append(mem, r.memMiB)
			cpu = append(cpu, mean(busy(r.cpuCores)))
		}
		fmt.Printf("| %s | %d | %s | %s | %s |\n", n, groups[n][0].Replicas, rng(conns, "%.0f"), rng(mem, "%.0f"), rng(cpu, "%.2f"))
	}
}

func list(xs []float64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%.0f", x)
	}
	return strings.Join(parts, ", ")
}

func rng(xs []float64, format string) string {
	if len(xs) == 0 {
		return ""
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	if len(s) == 1 {
		return fmt.Sprintf(format, s[0])
	}
	return fmt.Sprintf(format+" ("+format+"–"+format+")", median(s), s[0], s[len(s)-1])
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// phaseCPU estimates app CPU during phase i: busy samples are split across
// phases in proportion to their duration index (all S1 phases are 20 s), and
// the middle half of the slice is averaged to skip transitions.
func phaseCPU(r *run, i int) float64 {
	b := busy(r.cpuCores)
	n := len(r.phases)
	if len(b) < n {
		return mean(b)
	}
	per := len(b) / n
	s := b[i*per : (i+1)*per]
	return mean(s[len(s)/4 : len(s)-len(s)/4])
}

func busy(xs []float64) []float64 {
	var out []float64
	for _, x := range xs {
		if x > 0.05 {
			out = append(out, x)
		}
	}
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func load(dir string) (*run, error) {
	r := &run{}
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, r); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, "load.jsonl"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var p phase
		if json.Unmarshal(sc.Bytes(), &p) == nil {
			r.phases = append(r.phases, p)
		}
	}
	if len(r.phases) == 0 {
		return nil, fmt.Errorf("no phases")
	}
	return r, readStats(filepath.Join(dir, "stats.csv"), r)
}

// readStats parses "ts,name,cpu%,mem / limit" rows from docker stats and
// "ts,pg_connections,N," rows from the sampler.
func readStats(path string, r *run) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	perTS := map[string]float64{}
	var order []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), ",", 4)
		if len(parts) < 3 {
			continue
		}
		ts, name := parts[0], parts[1]
		switch {
		case name == "pg_connections":
			if n, err := strconv.Atoi(strings.TrimSpace(parts[2])); err == nil && n > r.pgConns {
				r.pgConns = n
			}
		case strings.HasPrefix(name, "bench-app-"):
			cpu, _ := strconv.ParseFloat(strings.TrimSuffix(parts[2], "%"), 64)
			if _, ok := perTS[ts]; !ok {
				order = append(order, ts)
			}
			perTS[ts] += cpu / 100
			if len(parts) == 4 {
				if m := parseMiB(strings.TrimSpace(strings.Split(parts[3], "/")[0])); m > r.memMiB {
					r.memMiB = m
				}
			}
		}
	}
	for _, ts := range order {
		r.cpuCores = append(r.cpuCores, perTS[ts])
	}
	return nil
}

func parseMiB(s string) float64 {
	units := []struct {
		suffix string
		mul    float64
	}{{"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024}, {"B", 1.0 / (1024 * 1024)}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			return v * u.mul
		}
	}
	return 0
}
