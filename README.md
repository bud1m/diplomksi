# Highly available RabbitMQ on Kubernetes, with a custom Go operator

This repository holds the practical part of the thesis. It has two phases.

**Phase 1** deploys a 3-node RabbitMQ cluster on Kubernetes. The cluster uses
quorum queues. A quorum queue replicates its messages with the Raft consensus
algorithm. RabbitMQ deprecated classic mirrored queues, so quorum queues are
the current standard for data safety.

**Phase 2** adds a custom Kubernetes operator, written in Go with Kubebuilder.
The operator reads one custom resource and provisions a complete messaging
setup for a microservice. See `operator/README.md`.

## What you need

Install these on your machine:

- Docker Desktop, or Docker Engine. Give it 12 GB of memory or more.
- `kind`, version 0.33 or later
- `kubectl`
- `helm`
- Go 1.23 or later, and `kubebuilder`. Phase 2 needs these. Phase 1 does not.

## How to run the demonstration

```bash
./setup-demo.sh
```

The script takes about 10 minutes on a first run. It is idempotent. If a step
fails, run the script again. It keeps the work that already succeeded.

To delete everything and start again, run two commands:

```bash
# 1. Destroy the environment
./setup-demo.sh --clean

# 2. Build it again
./setup-demo.sh
```

Open the RabbitMQ management UI at http://localhost:15672. Log in with
`admin` / `admin`.

The Cluster Operator also generates a random user for its own health probes.
Do not delete it. The script prints its name at the end.

## What the script builds

| Step | Component | Purpose |
|---|---|---|
| 1 | `kind` cluster | 1 control-plane node and 3 worker nodes |
| 2 | Broker image | Loads `rabbitmq:4.3.4-management` onto each worker |
| 3 | cert-manager | Issues the TLS certificate for the operator webhooks |
| 4 | RabbitMQ Cluster Operator | Manages the broker StatefulSet |
| 5 | `RabbitmqCluster` resource | The 3-node broker cluster itself |
| 6 | Placement check | Confirms one broker per worker node |
| 7 | Admin user | Creates `admin` / `admin` with the administrator tag |
| 8 | Chaos Mesh | Injects the faults for the experiments |
| 9 | Custom operator | Builds, loads and deploys Phase 2. Skipped if `operator/` is absent. |

## Why the configuration looks like this

**Three workers, not one.** Raft needs a majority. Three replicas survive the
loss of one replica, because two votes are still a majority. A single-node
cluster cannot show this.

**Pod anti-affinity is mandatory.** The Kubernetes scheduler can put two
brokers on one worker node. That node then holds two of the three Raft votes.
If it fails, the cluster loses its majority and the queue stops. The
`podAntiAffinity` rule in `infra/rabbitmq/rabbitmq-ha.yaml` prevents this.
Step 6 of the script checks the result and fails if the rule did not hold.

**Docker memory.** The script reads `docker info` and warns below 10 GB. On
Linux that number is the physical RAM of the host. On Docker Desktop it is the
limit of the virtual machine, and that is the number you must raise.

**The script pre-loads the broker image.** Three nodes that pull the same
image at the same time hit Docker Hub rate limits. A partial pull leaves the
node in `ImagePullBackOff`. The script pulls the image once and imports it
into each node. The demonstration then runs offline.

**cert-manager is a dependency, not an extra.** RabbitMQ Cluster Operator
2.23 serves admission webhooks over TLS. It reads its certificate from
cert-manager. Without cert-manager, the operator manifest fails to apply.

## How to check that it works

```bash
./verify-demo.sh
```

The script applies a profile and asserts what the broker really holds: four
quorum queues, three Raft members each, a scoped user, the Secret, and the
cleanup after a delete. It prints every assertion, so the output is evidence,
not a claim.

The Go tests run separately:

```bash
cd operator && go test ./...
```

## Fault injection

Apply an experiment, then watch the broker logs and the management UI.

```bash
# Cut one broker off from the other two. The remaining two keep the majority.
kubectl --context kind-diplomski-ha apply -f infra/chaos/network-partition.yaml

# Add 100 ms of latency between all brokers. Raft slows down but continues.
kubectl --context kind-diplomski-ha apply -f infra/chaos/network-delay.yaml

# Kill one broker pod. The StatefulSet restarts it and Raft catches it up.
kubectl --context kind-diplomski-ha apply -f infra/chaos/pod-kill-leader.yaml
```

To simulate a hardware failure, stop the Docker container of a worker node:

```bash
docker stop diplomski-ha-worker2
docker start diplomski-ha-worker2
```

## Repository layout

```
infra/
  kind/kind-ha-config.yaml       4-node cluster topology and host port mappings
  operators/cert-manager.yaml    Pinned cert-manager release
  operators/cluster-operator.yml Pinned RabbitMQ Cluster Operator release
  rabbitmq/rabbitmq-ha.yaml      The RabbitmqCluster custom resource
  chaos/                         Chaos Mesh experiments
operator/                        Phase 2. The custom Go operator.
setup-demo.sh                    Builds the whole environment
```

## Ports

The `kind` configuration maps two host ports to node ports. The demonstration
does not depend on `kubectl port-forward`, which drops its connection.

| Host port | Node port | Service |
|---|---|---|
| 15672 | 30672 | RabbitMQ management UI |
| 5672 | 30567 | AMQP |
