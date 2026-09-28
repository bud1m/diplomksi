# Shared helpers for the chaos experiments.
#
# Every script refuses to run outside the local kind cluster, so a destructive
# experiment can never reach a real cluster by accident.
set -euo pipefail

CTX="kind-diplomski-ha"
NS="messaging"
CLUSTER="rabbit-ha"
API="http://localhost:15672/api"
AUTH="admin:admin"
RESULTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/results"

k() { kubectl --context "$CTX" "$@"; }

guard() {
  local current
  current="$(kubectl config current-context 2>/dev/null || true)"
  if [[ "$current" != "$CTX" ]]; then
    echo "REFUSING: current context is '$current', expected '$CTX'." >&2
    echo "These experiments delete pods and stop nodes. Switch context first." >&2
    exit 1
  fi
}

log() { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }

# now_ms prints the wall clock in milliseconds.
now_ms() { python3 -c 'import time; print(int(time.time()*1000))'; }

# wait_all_brokers_ready blocks until all three brokers report Ready, and
# prints how many milliseconds that took.
wait_all_brokers_ready() {
  local start deadline ready
  start=$(now_ms)
  deadline=$(( $(date +%s) + 300 ))
  while [[ $(date +%s) -lt $deadline ]]; do
    ready=$(k -n "$NS" get pods -l "app.kubernetes.io/name=$CLUSTER" \
      -o jsonpath='{range .items[*]}{.status.containerStatuses[0].ready}{"\n"}{end}' \
      2>/dev/null | grep -c true || true)
    [[ "$ready" == "3" ]] && { echo $(( $(now_ms) - start )); return 0; }
    sleep 1
  done
  echo "-1"
  return 1
}

# queue_leader prints the node that currently holds the Raft leadership.
queue_leader() { # queue_leader <vhost> <queue>
  curl -fsS -u "$AUTH" "$API/queues/$1/$2" \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("leader",""))'
}

# queue_members prints how many Raft members the queue has.
queue_members() { # queue_members <vhost> <queue>
  curl -fsS -u "$AUTH" "$API/queues/$1/$2" \
    | python3 -c 'import json,sys; print(len(json.load(sys.stdin).get("members",[])))'
}
