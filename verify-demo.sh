#!/usr/bin/env bash
#
# End-to-end check of the custom operator against the live cluster.
#
# Run it after ./setup-demo.sh. It applies a profile, asserts what the broker
# really holds, deletes the profile and asserts the cleanup. Every assertion
# prints what it checked, so the output doubles as evidence for the thesis.
set -euo pipefail

CTX="kind-diplomski-ha"
NS="default"
PROFILE="orders-service"
VHOST="orders"
API="http://localhost:15672/api"
AUTH="admin:admin"

pass=0
fail=0

k() { kubectl --context "$CTX" "$@"; }

check() { # check <description> <expected> <actual>
  if [[ "$2" == "$3" ]]; then
    printf '  \033[32mPASS\033[0m %s (%s)\n' "$1" "$3"
    pass=$((pass + 1))
  else
    printf '  \033[31mFAIL\033[0m %s: expected %s, got %s\n' "$1" "$2" "$3"
    fail=$((fail + 1))
  fi
}

section() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }

# ---------------------------------------------------------------------------
section "Applying the profile"
k apply -f operator/config/samples/messaging_v1alpha1_microservicemessagingprofile.yaml

echo "    waiting for Ready"
for _ in $(seq 60); do
  [[ "$(k get mmp "$PROFILE" -n "$NS" \
      -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" == "True" ]] && break
  sleep 2
done

# ---------------------------------------------------------------------------
section "The profile reports itself ready"
check "Ready condition" "True" \
  "$(k get mmp "$PROFILE" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
check "reason" "Provisioned" \
  "$(k get mmp "$PROFILE" -n "$NS" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}')"

# ---------------------------------------------------------------------------
section "The broker holds real quorum queues"
QUEUES=$(curl -fsS -u "$AUTH" "$API/queues/$VHOST")

# Two queues from the sample, plus one dead letter queue each.
check "queue count" "4" "$(echo "$QUEUES" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
check "all are quorum queues" "yes" \
  "$(echo "$QUEUES" | python3 -c 'import json,sys; qs=json.load(sys.stdin); print("yes" if all(q["type"]=="quorum" for q in qs) else "no")')"
# Three replicas is the claim the whole thesis rests on.
check "all have 3 Raft members" "yes" \
  "$(echo "$QUEUES" | python3 -c 'import json,sys; qs=json.load(sys.stdin); print("yes" if all(len(q.get("members",[]))==3 for q in qs) else "no")')"
check "each main queue dead letters to its own dlq" "yes" \
  "$(echo "$QUEUES" | python3 -c '
import json,sys
qs={q["name"]: q for q in json.load(sys.stdin)}
ok=all(qs[n]["arguments"].get("x-dead-letter-routing-key")==n+".dlq"
       for n in qs if not n.endswith(".dlq"))
print("yes" if ok else "no")')"

# ---------------------------------------------------------------------------
section "The status reports the live Raft state"
check "every queue reports 3 replicas in status" "yes" \
  "$(k get mmp "$PROFILE" -n "$NS" -o json | python3 -c 'import json,sys; qs=json.load(sys.stdin)["status"]["queues"]; print("yes" if qs and all(q["replicas"]==3 for q in qs) else "no")')"
check "every queue names a Raft leader" "yes" \
  "$(k get mmp "$PROFILE" -n "$NS" -o json | python3 -c 'import json,sys; qs=json.load(sys.stdin)["status"]["queues"]; print("yes" if qs and all(q.get("leader") for q in qs) else "no")')"

# ---------------------------------------------------------------------------
section "The generated user is scoped to its own vhost"
USER=$(k get mmp "$PROFILE" -n "$NS" -o jsonpath='{.status.username}')
PERMS=$(curl -fsS -u "$AUTH" "$API/permissions/$VHOST/$USER")
check "cannot change topology" '"^$"' \
  "$(echo "$PERMS" | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["configure"]))')"
check "vhost count for this user" "1" \
  "$(curl -fsS -u "$AUTH" "$API/users/$USER/permissions" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"

# ---------------------------------------------------------------------------
section "The Secret is usable and owned"
SECRET=$(k get mmp "$PROFILE" -n "$NS" -o jsonpath='{.status.secretName}')
check "owner kind" "MicroserviceMessagingProfile" \
  "$(k get secret "$SECRET" -n "$NS" -o jsonpath='{.metadata.ownerReferences[0].kind}')"
check "AMQP_URI present" "yes" \
  "$(k get secret "$SECRET" -n "$NS" -o jsonpath='{.data.AMQP_URI}' | wc -c | awk '{print ($1>0)?"yes":"no"}')"

# ---------------------------------------------------------------------------
section "Reconciling repeatedly does not duplicate bindings"
BEFORE=$(curl -fsS -u "$AUTH" "$API/bindings/$VHOST" | python3 -c 'import json,sys; print(len([b for b in json.load(sys.stdin) if b["source"]]))')
for i in 1 2 3; do k annotate mmp "$PROFILE" -n "$NS" verify="$i" --overwrite >/dev/null; sleep 3; done
AFTER=$(curl -fsS -u "$AUTH" "$API/bindings/$VHOST" | python3 -c 'import json,sys; print(len([b for b in json.load(sys.stdin) if b["source"]]))')
# The management API creates bindings with POST, which is not idempotent.
check "binding count after 3 more reconciles" "$BEFORE" "$AFTER"

# ---------------------------------------------------------------------------
section "Deleting the profile revokes the credentials"
k delete mmp "$PROFILE" -n "$NS" --timeout=90s

check "profile gone" "0" \
  "$(k get mmp -n "$NS" --no-headers 2>/dev/null | grep -c "$PROFILE" || true)"
check "secret garbage collected" "no" \
  "$(k get secret "$SECRET" -n "$NS" >/dev/null 2>&1 && echo yes || echo no)"
check "broker user revoked" "404" \
  "$(curl -s -o /dev/null -w '%{http_code}' -u "$AUTH" "$API/users/$USER")"
check "queues removed" "0" \
  "$(curl -fsS -u "$AUTH" "$API/queues/$VHOST" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')"
# The vhost survives on purpose. A second profile may share it.
check "vhost kept" "200" \
  "$(curl -s -o /dev/null -w '%{http_code}' -u "$AUTH" "$API/vhosts/$VHOST")"

# ---------------------------------------------------------------------------
printf '\n\033[1m%d passed, %d failed\033[0m\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
