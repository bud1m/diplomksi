#!/usr/bin/env bash
#
# Run every experiment N times and keep each run separately.
#
#   ./experiments/run-repeated.sh [N]        default N = 3
#
# A single run of a chaos experiment is an anecdote. The numbers in chapter 6
# should be a median over repeats with the spread stated, so a reader can tell
# a stable result from a lucky one.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
guard

N="${1:-3}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNS="$RESULTS/runs"
mkdir -p "$RUNS"

EXPERIMENTS=(
  exp-01-broker-pod-failure
  exp-02-pdb-quorum-guard
  exp-03-node-failure
  exp-04-network-partition
  exp-05-network-delay
)

# Between runs the cluster must be back to three healthy brokers, otherwise the
# next run measures the tail of the previous one.
settle() {
  note "čekam da se klaster smiri"
  wait_all_brokers_ready >/dev/null || true
  for _ in $(seq 60); do
    [[ "$(queue_members orders orders.created 2>/dev/null)" == "3" ]] && break
    sleep 2
  done
  sleep 10
}

log "Ponavljam ${#EXPERIMENTS[@]} eksperimenta po $N puta"

for exp in "${EXPERIMENTS[@]}"; do
  for run in $(seq 1 "$N"); do
    log "$exp — prolaz $run/$N"
    if "$HERE/$exp.sh" >/dev/null 2>&1; then
      cp "$RESULTS/$exp.txt" "$RUNS/$exp.run$run.txt"
      note "sačuvano: runs/$exp.run$run.txt"
    else
      note "PROLAZ NIJE USPEO — preskačem"
      echo "run failed" > "$RUNS/$exp.run$run.txt"
    fi
    settle
  done
done

log "Sažimam rezultate"
python3 "$HERE/aggregate.py"
