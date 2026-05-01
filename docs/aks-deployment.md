# AKS Deployment Guide

This guide covers deploying Daedalus to the test AKS cluster provisioned by the
Terraform module in `deploy/terraform/`. Daedalus uses KEDA ScaledJobs for
scale-to-zero agent workers driven by NATS JetStream messages.

---

## Prerequisites

| Tool | Minimum version | Notes |
|------|----------------|-------|
| `az` (Azure CLI) | 2.50+ | For `az aks get-credentials` |
| `kubectl` | 1.28+ | Must point at the AKS cluster |
| `helm` | 3.12+ | Chart deployment and status |
| `terraform` | 1.7+ | Infrastructure provisioning |

Additionally:

- A GitHub token (`GITHUB_TOKEN`) with Copilot access is required at runtime.
- Container images must be pushed to `daedalustest.azurecr.io` before deploying.

---

## Architecture (AKS)

```
              NATS JetStream (in-cluster StatefulSet)
              stream: AGENT_TASKS
              subject: agent.tasks.copilot
                     |
                     | message arrives
                     v
          +--------------------+
          |  KEDA ScaledJob    |  (pollingInterval 15s)
          |  trigger: nats-    |  (lagThreshold 1)
          |  jetstream         |  (scale from 0 when lag > 0)
          +--------------------+
                     |
                     | creates one Job per message
                     v
          +-----------------------------+
          |  Kubernetes Job             |
          |  +--------+  +-----------+ |
          |  | proxy  |  |  agent    | |
          |  | (ACP   |  | (copilot- | |
          |  | client)|  |  bridge)  | |
          |  | :3000  |  | :3000     | |
          |  +--------+  +-----------+ |
          |  shared /workspace volume  |
          +-----------------------------+
                     |
                     | result published
                     v
              NATS subject: agent.results

  ACR: daedalustest.azurecr.io
  Images: daedalus-proxy, copilot-bridge
  imagePullSecrets: acr-pull-secret

  KEDA operator: installed by Terraform (keda namespace)
  NATS: Bitnami subchart, StatefulSet with JetStream enabled
```

---

## Quick Start

### 1. Provision the AKS cluster with Terraform

```bash
cd deploy/terraform/
terraform init
terraform apply -auto-approve
```

Copy the `get_credentials_command` output and run it:

```bash
az aks get-credentials --resource-group daedalus-test --name <cluster-name>
```

Verify:

```bash
kubectl get nodes
```

### 2. Push images to ACR

```bash
az acr login --name daedalustest

docker build -f deploy/docker/Dockerfile.proxy \
  -t daedalustest.azurecr.io/daedalus-proxy:latest .
docker push daedalustest.azurecr.io/daedalus-proxy:latest

docker pull ghcr.io/raykao/copilot-bridge:latest
docker tag ghcr.io/raykao/copilot-bridge:latest \
  daedalustest.azurecr.io/copilot-bridge:latest
docker push daedalustest.azurecr.io/copilot-bridge:latest
```

### 3. Create Kubernetes Secrets

Create the ACR pull secret (the service principal is created by Terraform; retrieve
client ID and secret from the Terraform outputs or Azure Portal):

```bash
kubectl create namespace daedalus --dry-run=client -o yaml | kubectl apply -f -

kubectl create secret docker-registry acr-pull-secret \
  --namespace daedalus \
  --docker-server=daedalustest.azurecr.io \
  --docker-username=<service-principal-client-id> \
  --docker-password=<service-principal-client-secret>
```

Create the Copilot token secret:

```bash
kubectl create secret generic copilot-secret \
  --namespace daedalus \
  --from-literal=github-token="${GITHUB_TOKEN}"
```

### 4. Deploy with Helm

```bash
make helm-aks-deploy
```

This runs:

```bash
helm upgrade --install daedalus deploy/helm/daedalus/ \
  -f deploy/helm/values-aks.yaml \
  --namespace daedalus \
  --create-namespace \
  --wait --timeout 5m
```

Verify the release:

```bash
make helm-aks-status
```

### 5. Run the validation script

```bash
export GITHUB_TOKEN="ghp_..."
./test/scripts/validate-aks-deployment.sh
```

---

## Configuration Reference

All fields in `deploy/helm/values-aks.yaml` and what they override from `values.yaml`:

| Field | AKS value | Default value | Reason |
|-------|-----------|---------------|--------|
| `keda.enabled` | `true` | `false` | Enable ScaledJobs; Deployments are not created when KEDA is enabled |
| `nats.enabled` | `true` | `true` | Use in-cluster NATS StatefulSet (explicit for clarity) |
| `imagePullSecrets[0].name` | `acr-pull-secret` | _(empty)_ | ACR requires authentication; the secret holds SP credentials |
| `proxy.image.repository` | `daedalustest.azurecr.io/daedalus-proxy` | `ghcr.io/raykao/daedalus-proxy` | Pull from private ACR instead of public ghcr.io |
| `proxy.image.pullPolicy` | `Always` | `IfNotPresent` | Ensures latest ACR push is used without tag changes |
| `workers[0].image.repository` | `daedalustest.azurecr.io/copilot-bridge` | `ghcr.io/raykao/copilot-bridge` | Pull from private ACR |
| `workers[0].image.pullPolicy` | `Always` | `IfNotPresent` | Same as proxy - always pull latest |
| `workers[0].proxy.resources.requests.cpu` | `100m` | `50m` | More headroom for real AKS node scheduling |
| `workers[0].proxy.resources.requests.memory` | `128Mi` | `64Mi` | More headroom for real AKS node scheduling |
| `workers[0].proxy.resources.limits.cpu` | `200m` | `200m` | Unchanged |
| `workers[0].proxy.resources.limits.memory` | `256Mi` | `128Mi` | Increased for ACP session overhead |
| `workers[0].resources.requests.cpu` | `200m` | `100m` | Copilot CLI JIT compilation needs CPU at startup |
| `workers[0].resources.requests.memory` | `256Mi` | `128Mi` | Increased for Copilot CLI runtime |
| `workers[0].resources.limits.cpu` | `1` (1 core) | `500m` | Allow burst during heavy inference calls |
| `workers[0].resources.limits.memory` | `1Gi` | `512Mi` | Copilot CLI can use significant memory during code generation |
| `workers[0].env[0]` | `GITHUB_TOKEN` from `copilot-secret` | _(empty)_ | Inject token from K8s Secret - never hardcode in values |

---

## What the Validation Script Tests

`test/scripts/validate-aks-deployment.sh` runs 10 steps:

1. **Prerequisites** - checks `kubectl`, `helm`, `GITHUB_TOKEN`, current kube context
2. **KEDA CRD** - verifies `scaledjobs.keda.sh` CRD is present (KEDA operator running)
3. **Helm release** - `helm status` confirms the release is in `deployed` state
4. **Scale-to-zero baseline** - confirms 0 Jobs exist when the queue is empty
5. **Publish test task** - uses `kubectl exec` into the NATS pod to publish a JSON-RPC 2.0 task to `agent.tasks.copilot`
6. **Cold-start timer** - polls for the first Job to appear; records latency from publish to first Job
7. **Job completion** - waits up to 120s for the Job to reach `Complete` status
8. **Result check** - subscribes to `agent.results` for 10s to verify a result was published
9. **Timing summary** - prints cold start, job execution, and end-to-end latencies with expected ranges
10. **Scale-to-zero restore** - waits up to 60s for Jobs to clear after completion

---

## Expected Behaviors

### KEDA ScaledJob Triggers

KEDA polls the NATS monitoring endpoint (`http://<release>-nats:8222`) every 15 seconds
(`keda.pollingInterval`). When the consumer lag for `agent.tasks.copilot` exceeds 0
(`activationLagThreshold: "0"`), KEDA creates one Job per pending message
(`lagThreshold: "1"`).

Each Job contains two containers:
- `proxy` - connects to NATS, dequeues one message, drives the agent via ACP, exits
- `agent` - listens on TCP port 3000, executes the task, exits when the proxy closes the connection

### Scale-to-Zero

When the NATS queue is empty:
- KEDA creates no Jobs
- No compute resources are consumed (other than the NATS StatefulSet itself)
- The first message after idle incurs a cold-start penalty

After all Jobs complete, KEDA stops creating new Jobs and the namespace returns to
zero running Jobs within 1-2 polling intervals (15-30s).

### Cold Start Latency

Expected range: **15-45 seconds** from message publish to first response.

Breakdown:
- KEDA polling delay: 0-15s (worst case is one full polling interval)
- Pod scheduling and image pull: 5-15s (image is cached after first pull, ACR is in the same Azure region)
- Agent (Copilot CLI) startup: ~5-10s (extension loading, GitHub auth handshake)
- Proxy ACP connection: ~1s

After the first Job runs on a node, the container image is cached. Subsequent Jobs
on the same node skip the pull step and start in 5-15s total.

### SIGTERM Graceful Shutdown

`terminationGracePeriodSeconds: 35` (established in Phase 0.4) covers two paths:

**Normal completion path:** If a task finishes before SIGTERM arrives, the proxy acks
the NATS message and exits cleanly. No requeue occurs.

**SIGTERM during active task:** The proxy sends an ACP `tasks/cancel` request to the
agent, waits for acknowledgement (up to 30s), then nacks the NATS message so it is
requeued for another worker. The 35s grace period covers this worst-case path (30s
agent drain + 5s buffer).

The 35s grace period is 5s longer than the ACP drain timeout (30s). Reducing it below
30s risks losing in-flight tasks without requeue.

---

## Troubleshooting

### KEDA not triggering (no Jobs appear after publishing)

**Symptom:** Task is published but no Job appears within 90s.

**Check 1 - KEDA operator logs:**
```bash
kubectl logs -n keda deploy/keda-operator --tail=50
```
Look for errors connecting to the NATS monitoring endpoint.

**Check 2 - ScaledJob status:**
```bash
kubectl describe scaledjob -n daedalus
```
Look for `TriggerAuthentication` or `ScaledJob` conditions indicating errors.

**Check 3 - NATS consumer:**
The KEDA NATS trigger requires a durable consumer named after the worker (e.g., `copilot`).
If the consumer was never created, KEDA cannot read the lag. Create it manually:
```bash
kubectl exec -n daedalus <nats-pod> -- \
  nats consumer add AGENT_TASKS copilot \
    --filter=agent.tasks.copilot \
    --durable=copilot \
    --ack=explicit \
    --deliver=all \
    --max-deliver=3
```

**Check 4 - Wrong stream name:**
Confirm `keda.natsStream` in `deploy/helm/daedalus/values.yaml` matches the stream
name in NATS (default: `AGENT_TASKS`). The AKS overlay (`values-aks.yaml`) does not
override this value.
```bash
kubectl exec -n daedalus <nats-pod> -- nats stream ls
```

### Image pull failures

**Symptom:** Pod stays in `ImagePullBackOff` or `ErrImagePull` state.

**Check 1 - Secret exists:**
```bash
kubectl get secret acr-pull-secret -n daedalus
```

**Check 2 - Service principal credentials are valid:**
```bash
az acr login --name daedalustest
docker pull daedalustest.azurecr.io/daedalus-proxy:latest
```

**Check 3 - Recreate the secret with fresh credentials:**
```bash
kubectl delete secret acr-pull-secret -n daedalus
kubectl create secret docker-registry acr-pull-secret \
  --namespace daedalus \
  --docker-server=daedalustest.azurecr.io \
  --docker-username=<new-client-id> \
  --docker-password=<new-client-secret>
```

### Pod crash loops (agent container exits immediately)

**Symptom:** Pod enters `CrashLoopBackOff`; agent container logs show
`GITHUB_TOKEN not set` or authentication errors.

**Check - Secret exists and has correct key:**
```bash
kubectl get secret copilot-secret -n daedalus -o jsonpath='{.data.github-token}' | base64 -d | head -c 10
```
Should print the first 10 characters of the token (`ghp_...` or `ghu_...`).

**Fix - Recreate the secret:**
```bash
kubectl delete secret copilot-secret -n daedalus
kubectl create secret generic copilot-secret \
  --namespace daedalus \
  --from-literal=github-token="${GITHUB_TOKEN}"
```

Then delete any failed pods; KEDA will create new Jobs on the next trigger.

### NATS JetStream stream not created

**Symptom:** The validation script fails at Step 5 with a NATS error like
`stream not found`.

The stream must exist before KEDA can read consumer lag. Create it manually:
```bash
kubectl exec -n daedalus <nats-pod> -- \
  nats stream add AGENT_TASKS \
    --subjects="agent.tasks.>" \
    --retention=limits \
    --max-msgs=-1 \
    --max-bytes=-1 \
    --max-age=1h \
    --storage=file \
    --replicas=1 \
    --discard=old
```

The validation script attempts to create the stream automatically (idempotent).

---

## Tearing Down

Remove only the Helm release (keeps the namespace and manually created secrets):

```bash
make helm-aks-teardown
```

Remove the namespace and all resources including secrets:

```bash
kubectl delete namespace daedalus
```

Destroy the AKS cluster and all Azure resources:

```bash
cd deploy/terraform/
terraform destroy -auto-approve
```
