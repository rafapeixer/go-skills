#!/usr/bin/env bash
# Runs every scenario for every implementation passed as arguments:
#   bash all.sh ../impl/go-without ../impl/go-with
set -uo pipefail
cd "$(dirname "$0")"

ramp="200:20s,400:20s,800:20s,1200:20s,1600:20s,2000:20s,2500:20s,3000:20s"

for impl in "$@"; do
  name=$(basename "$impl")
  # S1: capacity of one t4g.micro-sized replica.
  bash run.sh "$impl" "s1-capacity-$name" 1 "$ramp"
  # S2: 8 replicas behind the load balancer, steady load.
  bash run.sh "$impl" "s2-8replicas-$name" 8 "2000:60s"
  # S3: overload one replica at 2x a comfortable rate, then drop back.
  bash run.sh "$impl" "s3-overload-$name" 1 "800:20s,4000:30s,800:30s"
  # S4: the pricing dependency slows to 1.5 s for every call.
  bash run.sh "$impl" "s4-slowdep-$name" 1 "400:40s" 1500
done
