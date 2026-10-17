#!/usr/bin/env bash
# One benchmark run for one implementation.
#
#   bash run.sh <impl-dir> <label> <replicas> <phases> [pricing_latency_ms]
#
# Brings the stack up from scratch (fresh, seeded database), runs the contract
# tests, drives load from a container on the same network, samples CPU, memory
# and Postgres connections every second, and writes everything to
# results/<label>/. The stack is torn down at the end.
set -euo pipefail

cd "$(dirname "$0")"
impl=$(cd "$1" && pwd)
label=$2
replicas=$3
phases=$4
pricing_latency=${5:-20}
out=results/$label
mkdir -p "$out"

export IMPL=$impl
compose() { docker compose -p bench -f compose.yaml "$@"; }

compose down -v --remove-orphans >/dev/null 2>&1 || true
compose up -d --build --scale app="$replicas" >"$out/up.log" 2>&1
docker build -q -t bench-loadgen ./loadgen >/dev/null

network=bench_default
for i in $(seq 1 120); do
  docker run --rm --network $network curlimages/curl:8.11.1 -fsS http://lb:8080/healthz >/dev/null 2>&1 && break
  if [ "$i" = 120 ]; then
    echo "stack never became healthy for $label" >&2
    compose logs >"$out/stack.log" 2>&1
    compose down -v --remove-orphans >/dev/null 2>&1
    exit 1
  fi
  sleep 1
done
# Contract first, with the pricing service at its normal latency: an
# implementation that does not follow the spec is not benchmarked.
if ! (cd contract && BASE_URL=http://localhost:18080 go test -count=1 ./... >"../$out/contract.txt" 2>&1); then
  echo "contract FAILED for $label (see $out/contract.txt)"
  contract=failed
else
  contract=passed
fi

# Only then apply the scenario's pricing latency.
docker run --rm --network $network curlimages/curl:8.11.1 -fsS -X POST "http://pricing:8081/mode?latency_ms=$pricing_latency" >/dev/null

# Sample resources while the load runs.
(
  while true; do
    ts=$(date +%s)
    docker stats --no-stream --format "$ts,{{.Name}},{{.CPUPerc}},{{.MemUsage}}" 2>/dev/null
    conns=$(compose exec -T postgres psql -U postgres -d orders -tAc "select count(*) from pg_stat_activity where datname='orders' and backend_type='client backend'" 2>/dev/null || echo "")
    echo "$ts,pg_connections,$conns,"
    sleep 1
  done
) >"$out/stats.csv" &
sampler=$!

docker run --rm --network $network bench-loadgen -url http://lb:8080 -phases "$phases" -label "$label" >"$out/load.jsonl" 2>"$out/load.err" || true

kill $sampler 2>/dev/null || true
wait $sampler 2>/dev/null || true

compose logs app >"$out/app.log" 2>&1 || true
docker run --rm --network $network curlimages/curl:8.11.1 -sS -o /dev/null -w "%{http_code}\n" http://lb:8080/healthz >"$out/healthz-after.txt" 2>&1 || true
compose down -v --remove-orphans >/dev/null 2>&1

printf '{"label":"%s","impl":"%s","replicas":%s,"phases":"%s","pricing_latency_ms":%s,"contract":"%s"}\n' \
  "$label" "$1" "$replicas" "$phases" "$pricing_latency" "$contract" >"$out/meta.json"
echo "done $label: contract $contract"
