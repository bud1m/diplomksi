#!/usr/bin/env bash
#
# Experiment 3 - a whole worker node dies.
#
# Claim under test: the cluster survives the loss of a machine, not just the
# loss of a process. Each kind node is a Docker container, so `docker stop`
# is an honest simulation of a host that loses power: the kubelet stops
# reporting, the node goes NotReady, and its broker becomes unreachable
# without any graceful shutdown.
#
# This is the experiment a single-node k3d laboratory cannot perform.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
guard

OUT="$RESULTS/exp-03-node-failure.txt"

# Stop the node that currently runs the Raft leader.
LEADER_POD=$(queue_leader orders orders.created | sed 's/^rabbit@//; s/\..*//')
VICTIM_NODE=$(k -n "$NS" get pod "$LEADER_POD" -o jsonpath='{.spec.nodeName}')

log "Experiment 3: stop node $VICTIM_NODE, which runs the leader $LEADER_POD"

restore() {
  note "restarting $VICTIM_NODE"
  docker start "$VICTIM_NODE" >/dev/null
}
trap restore EXIT

curl -fsS -u "$AUTH" -H 'content-type: application/json' -XPUT \
  -d '{"configure":".*","write":".*","read":".*"}' \
  "$API/permissions/orders/admin" >/dev/null

note "starting publisher for 90 s"
python3 "$(dirname "${BASH_SOURCE[0]}")/publisher.py" 90 "$AUTH" > /tmp/exp03-publisher.json &
PUB=$!
sleep 10

note "docker stop $VICTIM_NODE"
STOP_AT=$(now_ms)
docker stop "$VICTIM_NODE" >/dev/null

# Kubernetes marks a silent node NotReady only after the node-monitor grace
# period, which is about 40 s by default. Reading the condition earlier than
# that reports a healthy node that is in fact already gone.
note "waiting for the control plane to notice the node is gone"
NODE_STATUS="True"
NOTICED_AFTER="-"
for i in $(seq 60); do
  NODE_STATUS=$(k get node "$VICTIM_NODE" \
    -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo Unknown)
  if [[ "$NODE_STATUS" != "True" ]]; then NOTICED_AFTER="$(( i ))s"; break; fi
  sleep 1
done
note "node Ready condition is now: $NODE_STATUS (after ${NOTICED_AFTER})"

MEMBERS_DURING=$(queue_members orders orders.created 2>/dev/null || echo "?")
LEADER_DURING=$(queue_leader orders orders.created 2>/dev/null || echo "?")
note "during the outage: $MEMBERS_DURING members, leader $LEADER_DURING"

wait $PUB

python3 - "$STOP_AT" "$VICTIM_NODE" "$LEADER_POD" "$LEADER_DURING" \
  "$MEMBERS_DURING" "$NODE_STATUS" "$NOTICED_AFTER" <<'PY' | tee "$OUT"
import json, sys, datetime
stop_at, node, leader_before, leader_during, members, node_status, noticed = sys.argv[1:8]
stop_at = int(stop_at) / 1000.0
d = json.load(open("/tmp/exp03-publisher.json"))
s = d["samples"]

before = [r for r in s if r["t"] <= stop_at and r["ok"]]
after = [r for r in s if r["t"] > stop_at and r["ok"]]
window = (after[0]["t"] - before[-1]["t"]) if before and after else None
post = [r for r in s if r["t"] > stop_at]
failed_after = [r for r in post if not r["ok"]]

# The gap to the first success hides the shape of the damage. What matters is
# whether the failures form one outage or are scattered retries, so measure
# the longest run of consecutive failures as well.
longest, run = 0.0, None
for r in post:
    if not r["ok"]:
        run = [r["t"], r["t"]] if run is None else [run[0], r["t"]]
        longest = max(longest, run[1] - run[0])
    else:
        run = None
rate = (100.0 * len(failed_after) / len(post)) if post else 0.0
kinds = {}
for r in failed_after:
    kinds[r.get("error", "?")] = kinds.get(r.get("error", "?"), 0) + 1

print("Experiment 3 - whole node failure")
print(f"  run at                 {datetime.datetime.now().isoformat(timespec='seconds')}")
print(f"  stopped node           {node}")
print(f"  it was running         {leader_before} (the Raft leader)")
print(f"  node Ready condition   {node_status}")
print(f"  publish attempts       {d['attempts']}")
print(f"  succeeded              {d['ok']}")
print(f"  failed                 {d['failed']}")
print(f"  failed after the stop  {len(failed_after)}")
print(f"  node marked NotReady   after {noticed}")
print(f"  gap to first success   {window*1000:.0f} ms" if window is not None
      else "  gap to first success   no successful publish after the stop")
print(f"  failure rate after     {rate:.1f}% of attempts")
print(f"  longest unbroken gap   {longest*1000:.0f} ms")
print(f"  failure kinds          {kinds if kinds else 'none'}")
print(f"  Raft members visible   {members}")
print(f"  leader during outage   {leader_during}")
verdict = "PASS" if (len(after) > 0 and leader_during not in ("", "?", leader_before)) else \
          ("PARTIAL" if len(after) > 0 else "FAIL")
print(f"  verdict                {verdict}")
PY
