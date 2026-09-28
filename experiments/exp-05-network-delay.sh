#!/usr/bin/env bash
#
# Experiment 5 - latency between brokers.
#
# Claim under test: added latency slows Raft down but does not break it. This
# is the control for Experiment 4: it separates "the link is slow" from "the
# link is gone", which are the two cases a consensus algorithm must tell apart.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
guard

OUT="$RESULTS/exp-05-network-delay.txt"
CHAOS="$(cd "$(dirname "${BASH_SOURCE[0]}")/../infra/chaos" && pwd)/network-delay.yaml"

log "Experiment 5: inject 100 ms of latency between brokers"

cleanup() { k delete -f "$CHAOS" --ignore-not-found >/dev/null 2>&1 || true; }
trap cleanup EXIT

curl -fsS -u "$AUTH" -H 'content-type: application/json' -XPUT \
  -d '{"configure":".*","write":".*","read":".*"}' \
  "$API/permissions/orders/admin" >/dev/null

LEADER_BEFORE=$(queue_leader orders orders.created)

note "starting publisher for 75 s"
python3 "$(dirname "${BASH_SOURCE[0]}")/publisher.py" 75 "$AUTH" > /tmp/exp05-publisher.json &
PUB=$!
sleep 10

note "applying NetworkChaos delay"
START_AT=$(now_ms)
k apply -f "$CHAOS" >/dev/null
sleep 20
PHASE=$(k -n "$NS" get networkchaos rabbit-network-delay \
  -o jsonpath='{.status.experiment.desiredPhase}' 2>/dev/null || echo "?")
MEMBERS_DURING=$(queue_members orders orders.created 2>/dev/null || echo "?")
note "chaos phase=$PHASE, $MEMBERS_DURING members during the delay"

wait $PUB
LEADER_AFTER=$(queue_leader orders orders.created 2>/dev/null || echo "?")

python3 - "$START_AT" "$LEADER_BEFORE" "$LEADER_AFTER" "$MEMBERS_DURING" "$PHASE" <<'PY' | tee "$OUT"
import json, sys, datetime
start_at, lb, la, md, phase = sys.argv[1:6]
start_at = int(start_at) / 1000.0
d = json.load(open("/tmp/exp05-publisher.json"))
s = d["samples"]
pre = [r for r in s if r["t"] <= start_at]
post = [r for r in s if r["t"] > start_at]
failed = [r for r in post if not r["ok"]]

print("Experiment 5 - 100 ms latency between brokers")
print(f"  run at                 {datetime.datetime.now().isoformat(timespec='seconds')}")
print(f"  chaos phase            {phase}")
print(f"  attempts before delay  {len(pre)}  (failed {sum(1 for r in pre if not r['ok'])})")
print(f"  attempts during delay  {len(post)}  (failed {len(failed)})")
print(f"  Raft members during    {md}")
print(f"  leader before          {lb}")
print(f"  leader after           {la}")
stable = "yes" if lb == la else "no - an election happened"
print(f"  leadership stable      {stable}")
verdict = "PASS" if (len(post) - len(failed) > 0 and md == "3") else "FAIL"
print(f"  verdict                {verdict}")
PY
