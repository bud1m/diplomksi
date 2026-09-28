#!/usr/bin/env bash
#
# Experiment 1 - a broker pod dies while clients are publishing.
#
# Claim under test: a quorum queue keeps accepting writes when one of its
# three replicas disappears, because the other two still form a majority.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
guard

OUT="$RESULTS/exp-01-broker-pod-failure.txt"

# Kill the Raft leader, not a follower. Losing a follower costs one vote and
# nothing else happens. Losing the leader forces an election, which is the
# case the thesis actually claims to survive.
# The leader reads as "rabbit@<pod>.rabbit-ha-nodes.messaging".
LEADER_POD=$(queue_leader orders orders.created | sed 's/^rabbit@//; s/\..*//')
VICTIM="${1:-$LEADER_POD}"

log "Experiment 1: kill the Raft leader ($VICTIM) under load"

note "baseline"
k -n "$NS" get pods -l "app.kubernetes.io/name=$CLUSTER" -o wide --no-headers
LEADER_BEFORE=$(queue_leader orders orders.created)
MEMBERS_BEFORE=$(queue_members orders orders.created)
note "queue orders.created: $MEMBERS_BEFORE members, leader $LEADER_BEFORE"

# Measure as admin. The user the operator generates carries no management
# tag on purpose, so it cannot use the HTTP API at all - it is an AMQP-only
# identity. That is the right security property, but it means the HTTP
# publisher has to authenticate as the administrator. Grant admin access to
# the tenant vhost for the duration of the measurement.
curl -fsS -u "$AUTH" -H 'content-type: application/json' -XPUT \
  -d '{"configure":".*","write":".*","read":".*"}' \
  "$API/permissions/orders/admin" >/dev/null
CREDS="$AUTH"

note "starting publisher for 60 s as admin"
python3 "$(dirname "${BASH_SOURCE[0]}")/publisher.py" 60 "$CREDS" > /tmp/exp01-publisher.json &
PUB=$!
sleep 10

note "deleting $VICTIM"
KILL_AT=$(now_ms)
k -n "$NS" delete pod "$VICTIM" --grace-period=0 --force >/dev/null 2>&1

RECOVERY_MS=$(wait_all_brokers_ready || true)
note "all three brokers Ready again after ${RECOVERY_MS} ms"

wait $PUB
LEADER_AFTER=$(queue_leader orders orders.created)
MEMBERS_AFTER=$(queue_members orders orders.created)

python3 - "$KILL_AT" "$RECOVERY_MS" "$LEADER_BEFORE" "$LEADER_AFTER" \
  "$MEMBERS_BEFORE" "$MEMBERS_AFTER" "$VICTIM" <<'PY' | tee "$OUT"
import json, sys, datetime
kill_at, recovery, lb, la, mb, ma, victim = sys.argv[1:8]
kill_at = int(kill_at) / 1000.0
d = json.load(open("/tmp/exp01-publisher.json"))
s = d["samples"]

# The outage window is the gap between the last success before the kill and
# the first success after it. Failures outside that gap would be a different
# problem and are counted separately.
before = [r for r in s if r["t"] <= kill_at and r["ok"]]
after = [r for r in s if r["t"] > kill_at and r["ok"]]
window = (after[0]["t"] - before[-1]["t"]) if before and after else None
failed_after = [r for r in s if r["t"] > kill_at and not r["ok"]]

print("Experiment 1 - broker pod failure")
print(f"  run at                 {datetime.datetime.now().isoformat(timespec='seconds')}")
print(f"  victim pod             {victim}")
print(f"  publish attempts       {d['attempts']}")
print(f"  succeeded              {d['ok']}")
print(f"  failed                 {d['failed']}")
print(f"  failed after the kill  {len(failed_after)}")
print(f"  client outage window   {window*1000:.0f} ms" if window is not None
      else "  client outage window   no successful publish after the kill")
print(f"  pod Ready again after  {recovery} ms")
print(f"  Raft members before    {mb}")
print(f"  Raft members after     {ma}")
print(f"  Raft leader before     {lb}")
print(f"  Raft leader after      {la}")
verdict = "PASS" if (d["ok"] > 0 and ma == "3") else "FAIL"
print(f"  verdict                {verdict}")
PY
