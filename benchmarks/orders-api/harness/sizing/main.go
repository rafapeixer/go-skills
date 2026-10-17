// Command sizing turns the measured capacity and CPU cost of each
// implementation into EC2 fleet sizes and monthly cost per load level.
//
//	go run ./sizing
//
// Model:
//   - Each instance runs one replica and is planned at 70% of its measured
//     capacity within the SLO (30% headroom for spikes and deploys).
//   - At least 2 instances per service, for availability.
//   - T4g instances run in unlimited mode (the AWS default): the hourly price
//     plus $0.04 per vCPU-hour of average CPU above the baseline
//     (t4g.micro 10% × 2 vCPU = 0.2; small and medium 20% × 2 = 0.4).
//   - 730 hours per month. Database cost is excluded: the queries are the
//     same for every implementation.
package main

import (
	"fmt"
	"math"
)

type impl struct {
	name        string
	capacityRPS float64 // highest rate within SLO, one replica, 2 vCPU
	cpuMsPerReq float64 // measured app CPU per request
	peakMemMiB  float64 // highest memory seen in any scenario
}

type instance struct {
	name     string
	memMiB   float64
	baseline float64 // cores
	price    map[string]float64
}

var impls = []impl{
	{"Go (with go-skills)", 5000, 0.305, 35},
	{"Java 25 + Spring Boot (warm JVM)", 3000, 0.453, 949},
	{"Python 3.13 + FastAPI", 1200, 0.924, 208},
}

var instances = []instance{
	{"t4g.micro", 1024, 0.2, map[string]float64{"us-east-1": 0.0084, "sa-east-1": 0.0134}},
	{"t4g.small", 2048, 0.4, map[string]float64{"us-east-1": 0.0168, "sa-east-1": 0.0268}},
	{"t4g.medium", 4096, 0.4, map[string]float64{"us-east-1": 0.0336, "sa-east-1": 0.0536}},
}

var loads = []float64{1000, 5000, 20000}

const (
	headroom   = 0.7
	surplus    = 0.04
	hoursMonth = 730
	minFleet   = 2
)

func main() {
	fmt.Println("| Load | Implementation | Instance | Fits in memory | Instances | CPU per instance | US$/month us-east-1 | US$/month sa-east-1 |")
	fmt.Println("|---|---|---|---|---|---|---|---|")
	for _, load := range loads {
		for _, im := range impls {
			for _, in := range instances {
				fits := im.peakMemMiB < in.memMiB*0.8
				n := math.Max(minFleet, math.Ceil(load/(im.capacityRPS*headroom)))
				cores := load / n * im.cpuMsPerReq / 1000
				fit := "yes"
				if !fits {
					fit = "**no** (peak " + fmt.Sprintf("%.0f", im.peakMemMiB) + " MiB)"
				}
				cost := func(region string) string {
					hourly := in.price[region] + math.Max(0, cores-in.baseline)*surplus
					return fmt.Sprintf("%.2f", hourly*hoursMonth*n)
				}
				fmt.Printf("| %.0f req/s | %s | %s | %s | %.0f | %.2f cores | %s | %s |\n",
					load, im.name, in.name, fit, n, cores, cost("us-east-1"), cost("sa-east-1"))
			}
		}
	}
}
