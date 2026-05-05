# AKS Deployment Guide

> **Happy path:** see [`docs/runbook.md`](runbook.md). This guide is the
> deeper reference: architecture, what the IaC stack provisions, configuration
> reference, expected behaviors, and troubleshooting. Use it when something
> breaks or when you need to know what is going on under `make deploy-aks-test`.

The Phase 5 IaC path supersedes the Phase 4 hand-driven flow. The historical
Phase 4 instructions are preserved verbatim in the
[appendix](#appendix-phase-4-manual-deployment-superseded).

---

## Architecture

```
                NATS JetStream (in-cluster StatefulSet)
                stream: AGENT_TASKS
                subject: agent.tasks.copilot
                       |
                       | message arrives
                       v
            +--------------------+
            |  KEDA ScaledJob    |  pollingInterval 15s
            |  trigger: nats-    |  lagThreshold 1
            |  jetstream         |  scale from 0 when lag > 0
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

  ACR pulls       : kubelet managed identity has AcrPull (no pull secret)
  Workload identity: federated to namespace=daedalus, sa=daedalus-proxy
                     (wired; Key Vault CSI mount deferred)
  Image publish   : GitHub Actions UAMI (federated OIDC) -> AcrPush
  TTL             : RG tagged auto-destroy=true, expires-at=<RFC3339>
                    (cleanup workflow reaps daily at 09:00 UTC)
```

Key Phase 5 differences from Phase 4:

- **No `acr-pull-secret`.** The AKS kubelet identity holds AcrPull on the ACR;
  pods pull directly with no `imagePullSecrets`.
- **No client secrets anywhere.** Image publish uses GitHub OIDC -> federated
  managed identity. Workload identity uses the AKS OIDC issuer.
- **TTL is the default disposal mechanism.** Manual `terraform destroy` works,
  but the expected lifecycle is "deploy, use, walk away, the cleanup workflow
  reaps it within 30 minutes of `expires-at`."

---

## What the Terraform stack provisions

Modules under `deploy/terraform/modules/`. See
[`deploy/terraform/README.md`](../deploy/terraform/README.md) for the full
reference.

- **`rg`** - workload resource group with TTL tags (`auto-destroy=true`,
  `expires-at=<RFC3339>`). Every Phase 5 resource lives here.
- **`aks`** - AKS cluster with OIDC issuer enabled and Workload Identity
  enabled. Default 2x `Standard_D2s_v5`, 75 GB managed OS disk,
  `upgrade_settings { max_surge = "1" }`.
- **`acr`** - Standard ACR. Grants AcrPull to the AKS kubelet identity.
  Optionally accepts `additional_push_principal_ids` for non-AKS pushers
  (used by `gha-identity` for AcrPush).
- **`keyvault`** - RBAC-mode Key Vault. Grants `Key Vault Secrets User` to the
  workload identity; grants `Key Vault Secrets Officer` to the deployer.
- **`identity`** - workload UAMI plus a federated credential bound to
  `system:serviceaccount:daedalus:daedalus-proxy`. The deploy script annotates
  the workload SA with this UAMI's client ID.
- **`gha-identity`** - separate UAMI plus federated credentials for the GitHub
  Actions OIDC subjects (default: `refs/heads/main`, `pull_request`,
  `environment:test`). Granted AcrPush on the ACR. When
  `enable_cleanup_role = true`, also granted subscription-scoped Contributor
  for the cleanup workflow.

---

## What `make deploy-aks-test` does

Mirrors the header comment in `deploy/scripts/deploy-aks.sh`. Each step is
idempotent; the whole script is safe to re-run.

1. **Prerequisites check** - verifies `az`, `terraform`, `kubectl`, `helm`, `jq`.
2. **Azure auth** - `az login --use-device-code` if not already signed in;
   `az account set --subscription` based on `subscription_id` from the tfvars.
3. **`terraform init` + `terraform apply`** - the AKS / ACR / Key Vault /
   identity / gha-identity stack. Re-applies extend the TTL.
4. **Read Terraform outputs** - cluster name, RG, ACR login server, KV URI,
   workload identity client ID, OIDC issuer, `expires_at`.
5. **`az aks get-credentials`** - writes kubeconfig and waits up to 5 min for
   at least one Ready node.
6. **Namespace upsert** - `kubectl create ns daedalus --dry-run=client | apply`.
7. **AcrPull sanity check (warn-only)** - looks up the AKS kubelet identity's
   role assignments on the ACR. Warns if AcrPull is missing.
8. **KEDA install** - `helm upgrade --install keda kedacore/keda` pinned to
   2.14.0 in the `keda` namespace.
9. **Workload Identity wiring** - labels the namespace
   `azure.workload.identity/use=true`. The SA annotation is applied in step 12
   (after helm install creates / leaves the `default` SA).
10. **`copilot-secret` upsert** - generic K8s secret with `github-token` key.
    Falls back to sourcing `smoke.env` if `GITHUB_TOKEN` is unset.
11. **ACR pull strategy** - re-confirms kubelet AcrPull. With it present, no
    pull secret is needed and `imagePullSecrets` stays empty.
12. **`helm upgrade --install daedalus`** - applies
    `deploy/helm/daedalus/values-aks-test.yaml` plus `--set` overrides for
    `proxy.image.repository`, `proxy.image.tag`, `workers[0].image.repository`,
    `workers[0].image.tag`. Then annotates the `default` SA in the namespace
    with `azure.workload.identity/client-id=<workload_identity_client_id>`.
13. **Smoke poll** - waits for the KEDA operator deployment, the NATS
    StatefulSet, and confirms at least one ScaledJob is registered.
14. **Summary** - prints cluster name, RG, ACR, namespace, release, image tag,
    `expires_at`, helm release status, and "next steps" hints.

---

## Configuration reference

`deploy/helm/daedalus/values-aks-test.yaml` is the test-environment overlay.
Image repositories are deliberate placeholders (`REPLACE_VIA_HELM_SET/...`)
overridden at apply time by `deploy/scripts/deploy-aks.sh` because each
engineer's ACR has a different hostname.

| Field                                          | Overlay value                                    | Chart default                       | Reason                                              |
| ---------------------------------------------- | ------------------------------------------------ | ----------------------------------- | --------------------------------------------------- |
| `keda.enabled`                                 | `true`                                           | `false`                             | Enable ScaledJob path; suppresses the Deployment.   |
| `nats.enabled`                                 | `true`                                           | `true`                              | In-cluster NATS StatefulSet (explicit for clarity). |
| `imagePullSecrets`                             | `[]`                                             | `[]`                                | Kubelet AcrPull; no docker-registry secret needed.  |
| `proxy.image.repository`                       | `REPLACE_VIA_HELM_SET/daedalus-proxy` (overridden) | `ghcr.io/raykao/daedalus-proxy`   | Pull from per-deployment ACR.                       |
| `proxy.image.tag`                              | `REPLACE_VIA_HELM_SET` (overridden via `IMAGE_TAG`) | (chart default)                  | Driven by `IMAGE_TAG` env var.                      |
| `proxy.image.pullPolicy`                       | `Always`                                         | `IfNotPresent`                      | Always pick up the latest ACR push.                 |
| `workers[0].image.repository`                  | `REPLACE_VIA_HELM_SET/copilot-bridge` (overridden) | `ghcr.io/raykao/copilot-bridge`   | Pull from per-deployment ACR.                       |
| `workers[0].image.tag`                         | `REPLACE_VIA_HELM_SET` (overridden)              | (chart default)                     | Driven by `IMAGE_TAG`.                              |
| `workers[0].image.pullPolicy`                  | `Always`                                         | `IfNotPresent`                      | Always pick up the latest ACR push.                 |
| `workers[0].proxy.resources.requests.cpu`      | `100m`                                           | `50m`                               | Headroom for AKS node scheduling.                   |
| `workers[0].proxy.resources.requests.memory`   | `128Mi`                                          | `64Mi`                              | Headroom for AKS node scheduling.                   |
| `workers[0].proxy.resources.limits.cpu`        | `200m`                                           | `200m`                              | Unchanged.                                          |
| `workers[0].proxy.resources.limits.memory`     | `256Mi`                                          | `128Mi`                             | ACP session overhead.                               |
| `workers[0].resources.requests.cpu`            | `200m`                                           | `100m`                              | Copilot CLI JIT compilation needs CPU at startup.   |
| `workers[0].resources.requests.memory`         | `256Mi`                                          | `128Mi`                             | Copilot CLI runtime baseline.                       |
| `workers[0].resources.limits.cpu`              | `1`                                              | `500m`                              | Burst headroom for inference.                       |
| `workers[0].resources.limits.memory`           | `1Gi`                                            | `512Mi`                             | Copilot CLI can use significant memory.             |
| `workers[0].env[0]` (`GITHUB_TOKEN`)           | from `copilot-secret`                            | _(empty)_                           | Inject token via secret; never hardcode in values.  |

---

## What the validation script tests

`test/scripts/validate-aks-deployment.sh` runs 10 steps:

1. **Prerequisites** - `kubectl`, `helm`, `GITHUB_TOKEN`, current kube context.
2. **KEDA CRD** - verifies `scaledjobs.keda.sh` is installed.
3. **Helm release** - `helm status` confirms `deployed` state.
4. **Scale-to-zero baseline** - confirms 0 Jobs when the queue is empty.
5. **Publish test task** - `kubectl exec` into the NATS pod; publishes a
   JSON-RPC 2.0 task to `agent.tasks.copilot`.
6. **Cold-start timer** - polls for the first Job; records publish-to-Job latency.
7. **Job completion** - waits up to 120s for `Complete`.
8. **Result check** - subscribes to `agent.results` for 10s.
9. **Timing summary** - prints cold start, exec, end-to-end latency and ranges.
10. **Scale-to-zero restore** - waits up to 60s for Jobs to clear.

The script's "deploy first" hints reference `make deploy-aks-test` (Phase 5).

---

## Expected behaviors

### KEDA ScaledJob triggers

KEDA polls the NATS monitoring endpoint (`http://<release>-nats:8222`) every
15 seconds (`keda.pollingInterval`). When the consumer lag for
`agent.tasks.copilot` exceeds 0 (`activationLagThreshold: "0"`), KEDA creates
one Job per pending message (`lagThreshold: "1"`).

Each Job contains two containers:

- `proxy` - connects to NATS, dequeues one message, drives the agent via ACP, exits.
- `agent` - listens on TCP port 3000, executes the task, exits when the proxy closes.

### Scale-to-zero

When the NATS queue is empty:

- KEDA creates no Jobs.
- No compute is consumed (other than the NATS StatefulSet itself).
- The first message after idle incurs a cold-start penalty.

After all Jobs complete, KEDA stops creating new Jobs. The namespace returns
to zero running Jobs within 1 to 2 polling intervals (15-30s).

### Cold start latency

Expected range: **15-45 seconds** from publish to first response.

Breakdown:

- KEDA polling delay: 0-15s (worst case is one full polling interval).
- Pod scheduling and image pull: 5-15s (cached after first pull; ACR is in
  the same Azure region).
- Agent (Copilot CLI) startup: ~5-10s (extension load, GitHub auth handshake).
- Proxy ACP connection: ~1s.

After the first Job runs on a node, the image is cached. Subsequent Jobs on
the same node start in 5-15s total.

### SIGTERM graceful shutdown

`terminationGracePeriodSeconds: 35` (Phase 0.4) covers two paths:

- **Normal completion:** the proxy acks the NATS message and exits cleanly.
  No requeue.
- **SIGTERM during active task:** the proxy sends ACP `tasks/cancel`, waits
  up to 30s for ack, then nacks the NATS message so it is requeued. The 35s
  grace is 30s drain + 5s buffer.

Reducing the grace below 30s risks losing in-flight tasks without requeue.

---

## Troubleshooting

### Terraform state lock

**Symptom:** `Error acquiring the state lock` on `terraform apply` or
`terraform destroy`. Often happens when a previous run was killed mid-apply
or when two engineers race against the same backend.

**Diagnosis:** the error message includes the lock ID, the holder identity,
and a `Created` timestamp. If the timestamp is recent and the holder is a
running operator, do not force-unlock.

**Fix:** confirm no concurrent apply is in flight, then:

```bash
terraform -chdir=deploy/terraform force-unlock <LOCK_ID>
```

Re-run `make deploy-aks-test`.

### AcrPull missing

**Symptom:** pods stuck in `ImagePullBackOff` or `ErrImagePull` even though
no `imagePullSecrets` are configured (Phase 5 design intent).

**Diagnosis:** the AKS kubelet identity does not have AcrPull on the ACR.
This should be impossible if Terraform applied cleanly, but role assignments
can be removed out-of-band, or the kubelet identity may have rotated.

```bash
KUBELET_OBJ_ID=$(az aks show -g <RG> -n <AKS_NAME> \
    --query identityProfile.kubeletidentity.objectId -o tsv)
ACR_ID=$(az acr show -n <ACR_NAME> --query id -o tsv)
az role assignment list --assignee "$KUBELET_OBJ_ID" --scope "$ACR_ID"
```

**Fix:** re-run Terraform; the `acr` module reconciles the role assignment.
As an emergency override:

```bash
az aks update -g <RG> -n <AKS_NAME> --attach-acr <ACR_NAME>
```

### Key Vault access denied

**Symptom:** a workload pod that uses the CSI driver (out of scope for Phase
5, but applicable when you wire it up later) gets a 403 reading a secret.

**Diagnosis:** the workload identity is missing `Key Vault Secrets User` on
the Key Vault, or the workload SA is not annotated with the correct UAMI
client ID, or the namespace is missing the `azure.workload.identity/use=true`
label.

**Fix:**

1. Re-run `terraform apply`; the `keyvault` module reconciles the role.
2. Confirm the SA annotation:
   ```bash
   kubectl get sa -n daedalus default -o yaml | grep client-id
   # must equal terraform output -raw workload_identity_client_id
   ```
3. Confirm the namespace label:
   ```bash
   kubectl get ns daedalus -o jsonpath='{.metadata.labels.azure\.workload\.identity/use}'
   # must print: true
   ```

### OIDC issuer not enabled (workload identity)

**Symptom:** federated credential refuses the workload identity token with
`AADSTS70021: No matching federated identity record found`.

**Diagnosis:** either the AKS OIDC issuer is disabled, or the federated
credential `subject` does not match `system:serviceaccount:<ns>:<sa>` exactly.

**Fix:**

- Confirm the cluster has OIDC enabled (Terraform sets `oidc_issuer_enabled = true`):
  ```bash
  az aks show -g <RG> -n <AKS_NAME> --query oidcIssuerProfile.enabled
  # must print: true
  ```
- Confirm the federated credential subject matches the SA:
  ```bash
  terraform -chdir=deploy/terraform output workload_identity_client_id
  # then list federated credentials on the workload UAMI:
  az identity federated-credential list \
      --identity-name <UAMI_NAME> -g <RG> -o table
  ```

  The `subject` must be `system:serviceaccount:daedalus:daedalus-proxy`
  (or whichever SA your workload uses).

### GitHub Actions OIDC subject mismatch

**Symptom:** `build-and-publish.yml` or `nightly-cleanup.yml` fails at the
`azure/login` step with `AADSTS70021: No matching federated identity record
found`.

**Diagnosis:** the GHA UAMI's federated subjects do not include the OIDC
`sub` claim the workflow run is presenting. GitHub sets `sub` based on the
trigger:

- `push` / `workflow_dispatch` on main: `repo:<owner>/<repo>:ref:refs/heads/main`
- `pull_request`: `repo:<owner>/<repo>:pull_request`
- A run inside an environment named `test`: `repo:<owner>/<repo>:environment:test`

**Fix:** compare the workflow's actual `sub` (visible in the OIDC token JWT
or in the `azure/login` debug output) against:

```bash
terraform -chdir=deploy/terraform output -json gha_oidc_subjects
```

If the missing subject is legitimate, add it via the `github_oidc_subjects`
tfvar. **The variable replaces the default list, it does not append**, so
include every existing subject plus the new one:

```hcl
github_oidc_subjects = [
  "repo:raykao/daedalus:ref:refs/heads/main",
  "repo:raykao/daedalus:pull_request",
  "repo:raykao/daedalus:environment:test",
  "repo:raykao/daedalus:ref:refs/heads/release/v1",
]
```

Then re-run `make deploy-aks-test` (Terraform reconciles the federated
credentials).

### KEDA not triggering (no Jobs appear after publishing)

**Symptom:** task is published but no Job appears within 90s.

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

The KEDA NATS trigger requires a durable consumer named after the worker
(e.g. `copilot`). If the consumer was never created, KEDA cannot read lag.
The validation script creates this idempotently; for a manual fix:

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

`keda.natsStream` in `deploy/helm/daedalus/values.yaml` must match the stream
name in NATS (default: `AGENT_TASKS`). The AKS overlay does not override it.

```bash
kubectl exec -n daedalus <nats-pod> -- nats stream ls
```

### Image pull failures

See [AcrPull missing](#acrpull-missing) above. With Phase 5, image pulls go
through the kubelet identity; there is no `acr-pull-secret` to recreate.

### Pod crash loops (agent container exits immediately)

**Symptom:** pod enters `CrashLoopBackOff`; the agent container logs show
`GITHUB_TOKEN not set` or auth errors.

**Check the secret has the correct key:**

```bash
kubectl get secret copilot-secret -n daedalus -o jsonpath='{.data.github-token}' \
    | base64 -d | head -c 10
```

Should print the first 10 characters of the token (`ghp_...` or `ghu_...`).

**Fix:** re-export `GITHUB_TOKEN` and re-run `make deploy-aks-test`. The
deploy script upserts the secret. KEDA will create new Jobs on the next
trigger.

### NATS JetStream stream not created

**Symptom:** the validation script fails at Step 5 with `stream not found`.

**Fix:** the validation script attempts idempotent creation. If you need to
do it by hand:

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

### See also

For Terraform-specific troubleshooting (TTL drift, cleanup role verification,
federated credential listing) see
[`deploy/terraform/README.md`](../deploy/terraform/README.md).

---

## Tearing down

```bash
make destroy-aks-test
```

Or wait for the Phase 5.5 cleanup workflow to delete the RG once `expires-at`
is in the past. See [runbook section 8](runbook.md#8-teardown).

---

## Appendix: Phase 4 manual deployment (superseded)

> This appendix preserves the Phase 4 hand-driven flow for historical
> reference. Phase 5 superseded it with `make deploy-aks-test`. Do not follow
> these steps for new deployments.

### A.1 Provision the AKS cluster with Terraform

```bash
cd deploy/terraform/
terraform init
terraform apply -auto-approve
```

Copy the `get_credentials_command` output and run it:

```bash
az aks get-credentials --resource-group daedalus-test --name <cluster-name>
```

### A.2 Push images to ACR by hand

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

### A.3 Create Kubernetes Secrets manually

Create the namespace, then create an ACR pull secret backed by a service
principal:

```bash
kubectl create namespace daedalus --dry-run=client -o yaml | kubectl apply -f -

ACR_ID=$(az acr show --name daedalustest --query id -o tsv)
az ad sp create-for-rbac --name daedalus-acr-pull \
  --role AcrPull \
  --scopes "${ACR_ID}"

kubectl create secret docker-registry acr-pull-secret \
  --namespace daedalus \
  --docker-server=daedalustest.azurecr.io \
  --docker-username=<service-principal-client-id> \
  --docker-password=<service-principal-client-secret>

kubectl create secret generic copilot-secret \
  --namespace daedalus \
  --from-literal=github-token="${GITHUB_TOKEN}"
```

### A.4 Deploy with Helm by hand

> The `-f` overlay (`deploy/helm/values-aks.yaml`) was deleted in Phase 5.6. The command below is preserved exactly as it ran in Phase 4; reproducing it today requires restoring that file from git history.

```bash
helm upgrade --install daedalus deploy/helm/daedalus/ \
    -f deploy/helm/values-aks.yaml \
    --namespace daedalus \
    --create-namespace \
    --wait --timeout 5m
```

The Phase 4 Make targets that wrapped this call (`helm-` prefixed
`aks-deploy` / `aks-teardown` / `aks-status`) were removed in Phase 5.6
in favor of `make deploy-aks-test` / `make destroy-aks-test` / `make
aks-status`. The overlay file itself was deleted in Phase 5.6 in favor
of `deploy/helm/daedalus/values-aks-test.yaml`, which the Phase 5 deploy
script overrides via `--set`.

### A.5 Run the validation script

```bash
export GITHUB_TOKEN="ghp_..."
./test/scripts/validate-aks-deployment.sh
```
