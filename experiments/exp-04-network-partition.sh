#!/usr/bin/env bash
#
# Experiment 4 - network partition.
#
# Claim under test: a quorum queue keeps accepting writes when one replica is
# cut off from the other two, because the remaining two still hold a Raft
# majority. This is the scenario quorum queues exist for, and the one the
# removed mirrored queues handled incorrectly: a mirrored queue could accept
# writes on both sides of a split and silently lose one of them.
#
# Unlike killing a pod, a partitioned broker is still running and still
# believes it may be the leader. That is what makes this the honest test.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
guard

OUT="$RESULTS/exp-04-network-partition.txt"
CHAOS="$(cd "$(dirname "${BASH_SOURCE[0]}")/../infra/chaos" && pwd)/network-partition.yaml"

LEADER_POD=$(queue_leader orders orders.created | sed 's/^rabbit@//; s/\..*//')
log "Experiment 4: partition one broker. Current leader is $LEADER_POD"

cleanup() {
  note "removing the chaos experiment"
  k delete -f "$CHAOS" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

curl -fsS -u "$AUTH" -H 'content-type: application/json' -XPUT \
  -d '{"configure":".*","write":".*","read":".*"}' \
  "$API/permissions/orders/admin" >/dev/null

note "starting publisher for 90 s"
python3 "$(dirname "${BASH_SOURCE[0]}")/publisher.py" 90 "$AUTH" > /tmp/exp04-publisher.json &
PUB=$!
sleep 10

note "applying NetworkChaos partition"
START_AT=$(now_ms)
k apply -f "$CHAOS" >/dev/null

# Let the experiment run, then read what the broker thinks while it is split.
sleep 25
PHASE=$(k -n "$NS" get networkchaos rabbit-partition-one-node \
  -o jsonpath='{.status.experiment.desiredPhase}' 2>/dev/null || echo "?")
INJECTED=$(k -n "$NS" get networkchaos rabbit-partition-one-node \
  -o jsonpath='{.status.experiment.containerRecords[*].injectedCount}' 2>/dev/null || echo "?")
note "chaos phase=$PHASE injected=$INJECTED"

MEMBERS_DURING=$(queue_members orders orders.created 2>/dev/null || echo "?")
LEADER_DURING=$(queue_leader orders orders.created 2>/dev/null || echo "?")
note "during the split: $MEMBERS_DURING members, leader $LEADER_DURING"

# Does Kubernetes notice that a partitioned broker is useless? The readiness
# probe opens a TCP socket from the kubelet on the same node, so it never
# crosses the partition. If every pod still reports Ready, the Service keeps
# sending clients to a broker that cannot reach its peers.
READY_DURING=$(k -n "$NS" get pods -l "app.kubernetes.io/name=$CLUSTER" \
  -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{" "}{end}')
ENDPOINTS_DURING=$(k -n "$NS" get endpoints "$CLUSTER" \
  -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null | wc -w | tr -d ' ')
note "pods Ready during the split: $READY_DURING"
note "Service endpoints during the split: $ENDPOINTS_DURING"

wait $PUB
cleanup
trap - EXIT
sleep 10
LEADER_AFTER=$(queue_leader orders orders.created 2>/dev/null || echo "?")
MEMBERS_AFTER=$(queue_members orders orders.created 2>/dev/null || echo "?")

python3 - "$START_AT" "$LEADER_POD" "$LEADER_DURING" "$LEADER_AFTER" \
  "$MEMBERS_DURING" "$MEMBERS_AFTER" "$PHASE" "$INJECTED" \
  "$READY_DURING" "$ENDPOINTS_DURING" <<'PY' | tee "$OUT"
import json, sys, datetime
start_at, lb, ld, la, md, ma, phase, injected, ready, endpoints = sys.argv[1:11]
start_at = int(start_at) / 1000.0
d = json.load(open("/tmp/exp04-publisher.json"))
s = d["samples"]
post = [r for r in s if r["t"] > start_at]
failed = [r for r in post if not r["ok"]]

longest, run = 0.0, None
for r in post:
    if not r["ok"]:
        run = [r["t"], r["t"]] if run is None else [run[0], r["t"]]
        longest = max(longest, run[1] - run[0])
    else:
        run = None
kinds = {}
for r in failed:
    kinds[r.get("error", "?")] = kinds.get(r.get("error", "?"), 0) + 1

print("Experiment 4 - network partition")
print(f"  run at                 {datetime.datetime.now().isoformat(timespec='seconds')}")
print(f"  chaos phase            {phase}  (injected containers: {injected})")
print(f"  publish attempts       {d['attempts']}")
print(f"  succeeded              {d['ok']}")
print(f"  failed                 {d['failed']}")
print(f"  attempts after split   {len(post)}")
print(f"  failed after split     {len(failed)}  ({100.0*len(failed)/len(post):.1f}%)" if post else "  no attempts after split")
print(f"  longest unbroken gap   {longest*1000:.0f} ms")
print(f"  failure kinds          {kinds if kinds else 'none'}")
print(f"  Raft members before    3")
print(f"  Raft members during    {md}")
print(f"  Raft members after     {ma}")
print(f"  leader before          {lb}")
print(f"  leader during          {ld}")
print(f"  leader after           {la}")
print(f"  pods Ready during      {ready.strip()}")
print(f"  Service endpoints      {endpoints}")
# The queue must stay writable and must never drop below three members: a
# partitioned replica is cut off, not removed from the Raft configuration.
verdict = "PASS" if (len(post) - len(failed) > 0 and md == "3") else "FAIL"
print(f"  verdict                {verdict}")
PY
