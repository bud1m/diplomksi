#!/usr/bin/env bash
#
# One-click environment for the thesis demonstration.
#
#   ./setup-demo.sh          build everything
#   ./setup-demo.sh --clean  delete the cluster and start over
#
# The script is idempotent: every step checks whether its work is already done,
# so you can re-run it after an interrupted attempt without deleting anything.
set -euo pipefail

CLUSTER_NAME="diplomski-ha"
CTX="kind-${CLUSTER_NAME}"
NAMESPACE="messaging"
RABBIT_CLUSTER="rabbit-ha"
RABBIT_IMAGE="rabbitmq:4.3.4-management"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log()  { printf '\n\033[1;36m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m    %s\033[0m\n' "$*"; }
k()    { kubectl --context "$CTX" "$@"; }

# Copy a local image into the containerd store of every worker node.
# `kind load docker-image` is not used: it passes --all-platforms, so a
# single-architecture local image fails the import with a missing-layer error.
load_image_into_workers() {
  local image="$1" tar node
  tar="$(mktemp -t kind-image).tar"
  docker save "$image" -o "$tar"
  for node in $(kind get nodes --name "$CLUSTER_NAME" | grep -v control-plane); do
    printf '    %s ... ' "$node"
    docker exec --privileged -i "$node" \
      ctr --namespace=k8s.io images import --digests --snapshotter=overlayfs - \
      < "$tar" >/dev/null
    echo "ok"
  done
  rm -f "$tar"
}

if [[ "${1:-}" == "--clean" ]]; then
  log "Deleting cluster ${CLUSTER_NAME}"
  kind delete cluster --name "$CLUSTER_NAME"
  exit 0
fi

# ---------------------------------------------------------------------------
log "0/9 Checking prerequisites"
for bin in docker kind kubectl helm; do
  command -v "$bin" >/dev/null || { echo "missing: $bin"; exit 1; }
done
docker info >/dev/null 2>&1 || { echo "Docker is not running"; exit 1; }

# Four Kubernetes nodes plus three brokers plus Chaos Mesh do not fit in the
# 8 GB Docker Desktop ships with by default. Warn rather than fail, because the
# number is only readable on Docker Desktop.
DOCKER_MEM_GB=$(( $(docker info --format '{{.MemTotal}}') / 1024 / 1024 / 1024 ))
if (( DOCKER_MEM_GB < 10 )); then
  # On Linux this number is the physical RAM of the host. On Docker Desktop it
  # is the limit of the virtual machine, which is the number that matters.
  warn "Docker reports ${DOCKER_MEM_GB} GB of memory."
  warn "On macOS or Windows, give Docker Desktop 12-16 GB in"
  warn "Settings > Resources, or the cluster will thrash."
fi

# ---------------------------------------------------------------------------
log "1/9 Creating the ${CLUSTER_NAME} cluster (1 control-plane + 3 workers)"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  warn "cluster already exists, keeping it"
  # The nodes can outlive the kubeconfig entry, for example after someone
  # rewrites ~/.kube/config. Write the context back before using it.
  kind export kubeconfig --name "$CLUSTER_NAME" >/dev/null
else
  kind create cluster --config "${REPO_ROOT}/infra/kind/kind-ha-config.yaml"
fi
k wait --for=condition=Ready nodes --all --timeout=180s

# ---------------------------------------------------------------------------
log "2/9 Pre-loading ${RABBIT_IMAGE} onto every worker"
# Pulling the broker image separately on three nodes at once is the single most
# unreliable step in this setup: Docker Hub rate limits and half-written layers
# leave nodes in ImagePullBackOff. Pulling once on the host and importing the
# tarball makes the demo deterministic and lets it run offline.
docker image inspect "$RABBIT_IMAGE" >/dev/null 2>&1 \
  || docker pull --platform "linux/$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')" "$RABBIT_IMAGE"

load_image_into_workers "$RABBIT_IMAGE"

# ---------------------------------------------------------------------------
log "3/9 Installing cert-manager"
# RabbitMQ Cluster Operator 2.23+ serves admission webhooks over TLS and gets
# its certificate from cert-manager, so this is a hard dependency, not an extra.
if k get deploy -n cert-manager cert-manager >/dev/null 2>&1; then
  warn "already installed"
else
  k apply -f "${REPO_ROOT}/infra/operators/cert-manager.yaml"
fi
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  k -n cert-manager rollout status "deploy/$d" --timeout=300s
done

# ---------------------------------------------------------------------------
log "4/9 Installing the RabbitMQ Cluster Operator"
k apply -f "${REPO_ROOT}/infra/operators/cluster-operator.yml"
k -n rabbitmq-system rollout status deploy/rabbitmq-cluster-operator --timeout=300s

# ---------------------------------------------------------------------------
log "5/9 Deploying the 3-node RabbitMQ HA cluster"
k apply -f "${REPO_ROOT}/infra/rabbitmq/rabbitmq-ha.yaml"

# The Cluster Operator creates the StatefulSet a few seconds after the custom
# resource appears. Wait for the object to exist before you wait on its
# rollout, or kubectl reports NotFound and the script stops.
echo "    waiting for the operator to create the StatefulSet"
for _ in $(seq 60); do
  k -n "$NAMESPACE" get "sts/${RABBIT_CLUSTER}-server" >/dev/null 2>&1 && break
  sleep 2
done

echo "    waiting for all three brokers to form a cluster (a few minutes)"
k -n "$NAMESPACE" rollout status "sts/${RABBIT_CLUSTER}-server" --timeout=600s

# ---------------------------------------------------------------------------
log "6/9 Verifying one broker per worker node"
# If two brokers share a node, losing that node costs two of three Raft votes
# and the quorum-queue demonstration proves the opposite of the thesis. Fail
# loudly rather than demo a cluster that is not really highly available.
PLACEMENT=$(k -n "$NAMESPACE" get pods -l "app.kubernetes.io/name=${RABBIT_CLUSTER}" \
  -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}')
echo "$PLACEMENT" | sed 's/^/    /'
if [[ $(echo "$PLACEMENT" | sort -u | wc -l) -ne $(echo "$PLACEMENT" | wc -l) ]]; then
  echo "FAIL: two brokers landed on the same node; check the podAntiAffinity rule"
  exit 1
fi
echo "    ok - one broker per node"

# ---------------------------------------------------------------------------
log "7/9 Creating the admin user"
# The Cluster Operator generates a random user for its own health checks. Do
# not replace it. Add a second user with a name you can type at the podium,
# because nobody wants to read a 23-character password off a slide.
RMQ_EXEC="k -n ${NAMESPACE} exec ${RABBIT_CLUSTER}-server-0 -c rabbitmq --"
# rabbitmqctl prints a box-drawing table, so parsing list_users is brittle.
# Try to add the user and fall back to a password reset if it already exists.
$RMQ_EXEC rabbitmqctl add_user admin admin >/dev/null 2>&1 \
  || $RMQ_EXEC rabbitmqctl change_password admin admin >/dev/null
$RMQ_EXEC rabbitmqctl set_user_tags admin administrator
$RMQ_EXEC rabbitmqctl set_permissions -p / admin ".*" ".*" ".*"
echo "    ok - admin / admin"

# ---------------------------------------------------------------------------
log "8/9 Installing Chaos Mesh"
# kind nodes run containerd, not Docker, so chaos-daemon has to be pointed at
# the containerd socket. The default Helm values assume Docker and the daemon
# silently fails to inject anything.
if k get ns chaos-mesh >/dev/null 2>&1; then
  warn "already installed"
else
  helm repo add chaos-mesh https://charts.chaos-mesh.org >/dev/null 2>&1 || true
  helm repo update chaos-mesh >/dev/null
  k create namespace chaos-mesh
  helm install chaos-mesh chaos-mesh/chaos-mesh \
    --kube-context "$CTX" \
    --namespace chaos-mesh \
    --set chaosDaemon.runtime=containerd \
    --set chaosDaemon.socketPath=/run/containerd/containerd.sock \
    --set dnsServer.create=false \
    --wait --timeout 10m
fi
# Helm --wait covers the Deployments. The privileged DaemonSet that does the
# actual fault injection is not always ready when Helm returns, and an
# experiment fired too early silently injects nothing.
k -n chaos-mesh rollout status daemonset/chaos-daemon --timeout=300s

# ---------------------------------------------------------------------------
log "9/9 Building and deploying the custom operator"
# Phase 2. The repository ships Phase 1 on its own, so skip this step until
# the operator exists rather than fail the whole environment.
if [[ -f "${REPO_ROOT}/operator/Makefile" ]]; then
  OPERATOR_IMAGE="thesis-operator:local"
  ( cd "${REPO_ROOT}/operator" && make docker-build IMG="$OPERATOR_IMAGE" )
  load_image_into_workers "$OPERATOR_IMAGE"
  ( cd "${REPO_ROOT}/operator" && make deploy IMG="$OPERATOR_IMAGE" )
  k -n messaging-operator-system rollout status \
    deploy/messaging-operator-controller-manager --timeout=300s
else
  warn "operator/ is not present yet, skipping Phase 2"
fi

# ---------------------------------------------------------------------------
OPERATOR_USER=$(k -n "$NAMESPACE" get secret "${RABBIT_CLUSTER}-default-user" -o jsonpath='{.data.username}' | base64 -d)

printf '\n\033[1;32m==> Environment ready\033[0m\n'; cat <<EOF



  RabbitMQ management UI   http://localhost:15672
  AMQP endpoint            amqp://localhost:5672
  Username                 admin
  Password                 admin

  The operator keeps its own generated user for the health probes. Leave it
  alone: ${OPERATOR_USER}

  Chaos Mesh dashboard     kubectl --context ${CTX} -n chaos-mesh port-forward svc/chaos-dashboard 2333:2333

  Fault injection:
    kubectl --context ${CTX} apply -f infra/chaos/network-partition.yaml
    kubectl --context ${CTX} apply -f infra/chaos/pod-kill-leader.yaml
  Node failure, the blunt version:
    docker stop diplomski-ha-worker2

  Start over:  ./setup-demo.sh --clean && ./setup-demo.sh
EOF
