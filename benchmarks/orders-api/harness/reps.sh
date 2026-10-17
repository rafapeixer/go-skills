#!/usr/bin/env bash
# Every scenario in BENCHMARKS.md for one repetition:
#   bash reps.sh r2
# Results land in results/<scenario>-<impl>-<rep>/.
set -uo pipefail
cd "$(dirname "$0")"
rep=$1

ramp="200:20s,400:20s,800:20s,1200:20s,1600:20s,2000:20s,2500:20s,3000:20s"
high="3000:20s,4000:20s,5000:20s,6000:20s,8000:20s,10000:20s"
warm="500:30s,1500:30s,3000:30s,4000:20s,5000:20s,6000:20s,8000:20s"

for impl in go-with go-without java python; do
  bash run.sh ../impl/$impl "s1-capacity-$impl-$rep" 1 "$ramp"
  bash run.sh ../impl/$impl "s2-8replicas-$impl-$rep" 8 "2000:60s"
  bash run.sh ../impl/$impl "s3-overload-$impl-$rep" 1 "800:20s,4000:30s,800:30s"
  bash run.sh ../impl/$impl "s4-slowdep-$impl-$rep" 1 "400:40s" 1500
done
for impl in go-with go-without java; do
  bash run.sh ../impl/$impl "s5-high-$impl-$rep" 1 "$high"
done
bash run.sh ../impl/java "s7-warm-high-java-$rep" 1 "$warm"
