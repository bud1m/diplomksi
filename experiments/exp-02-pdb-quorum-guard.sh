#!/usr/bin/env bash
#
# Experiment 2 - the PodDisruptionBudget refuses to break the Raft majority.
#
# Claim under test: Kubernetes will not voluntarily evict a second broker
# while the first is still away, because two of three replicas must stay up
# for a quorum queue to accept writes.
#
# The test calls the eviction API directly rather than running `kubectl drain`.
# With three brokers on three nodes and a required anti-affinity rule, an
# evicted broker has no fourth node to move to, so drain blocks on scheduling
# and hides the behaviour of the budget.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
guard

OUT="$RESULTS/exp-02-pdb-quorum-guard.txt"
log "Experiment 2: eviction against the PodDisruptionBudget"

ALLOWED=$(k -n "$NS" get pdb "$CLUSTER" -o jsonpath='{.status.disruptionsAllowed}')
note "budget reports disruptionsAllowed=$ALLOWED"

evict() { # evict <pod> -> prints the HTTP status
  k -n "$NS" get pod "$1" >/dev/null 2>&1 || { echo "000"; return; }
  k proxy --port=18001 >/dev/null 2>&1 &
  local proxy=$!
  sleep 2
  local code
  code=$(curl -s -o /tmp/evict-body.json -w '%{http_code}' \
    -H 'Content-Type: application/json' \
    -d "{\"apiVersion\":\"policy/v1\",\"kind\":\"Eviction\",\"metadata\":{\"name\":\"$1\",\"namespace\":\"$NS\"}}" \
    "http://127.0.0.1:18001/api/v1/namespaces/$NS/pods/$1/eviction")
  kill $proxy 2>/dev/null || true
  wait $proxy 2>/dev/null || true
  echo "$code"
}

FIRST=$(evict rabbit-ha-server-0)
note "first eviction  (rabbit-ha-server-0): HTTP $FIRST"

# Immediately try a second one, while the first broker is still restarting.
SECOND=$(evict rabbit-ha-server-2)
SECOND_MSG=$(python3 -c 'import json;print(json.load(open("/tmp/evict-body.json")).get("message",""))' 2>/dev/null || true)
note "second eviction (rabbit-ha-server-2): HTTP $SECOND"

{
  echo "Experiment 2 - PodDisruptionBudget and the Raft majority"
  echo "  run at                 $(date -Iseconds)"
  echo "  minAvailable           $(k -n "$NS" get pdb "$CLUSTER" -o jsonpath='{.spec.minAvailable}')"
  echo "  disruptionsAllowed     $ALLOWED"
  echo "  first eviction         HTTP $FIRST  (expected 201, allowed)"
  echo "  second eviction        HTTP $SECOND  (expected 429, refused)"
  echo "  refusal message        $SECOND_MSG"
  if [[ "$FIRST" == "201" && "$SECOND" == "429" ]]; then
    echo "  verdict                PASS"
  else
    echo "  verdict                FAIL"
  fi
} | tee "$OUT"

note "waiting for the cluster to settle"
wait_all_brokers_ready >/dev/null || true
