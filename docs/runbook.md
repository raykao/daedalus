# Daedalus Deployment Runbook

> **Goal:** Deploy Daedalus to AKS with KEDA scaling in under 30 minutes.
> Full reference: [AKS Deployment Guide](aks-deployment.md)

---

## Prerequisites Checklist

- [ ] `az` CLI 2.50+ (`az --version`)
- [ ] `kubectl` 1.28+ (`kubectl version --client`)
- [ ] `helm` 3.12+ (`helm version`)
- [ ] `terraform` 1.5+ (`terraform --version`)
- [ ] Docker (for image builds)
- [ ] GitHub token with Copilot access
- [ ] Azure subscription with Contributor access to a resource group
- [ ] Cloned repo at the project root

---

## Part 1: Provision Infrastructure (~10 min)

**1.** Log in to Azure and set your target subscription.

```bash
az login
az account set --subscription <SUBSCRIPTION_ID>
```

**2.** Initialise and apply the Terraform module. The `deploy` target runs in two phases: AKS/ACR first, then KEDA.

```bash
cd deploy/terraform/
make deploy
```

> **Note:** Phase 1 provisions the AKS cluster, ACR, and resource group. Phase 2 installs the KEDA operator once the cluster is available. Both phases run automatically with `make deploy`.

**3.** Capture the Terraform outputs you will need in later steps.

```bash
terraform output get_credentials_command   # copy the printed command
terraform output acr_login_server          # note this value - used in Parts 2 and 3
```

Example output:
```
get_credentials_command = "az aks get-credentials --resource-group daedalus-rg --name daedalus-aks"
acr_login_server = "daedalustest.azurecr.io"
```

**4.** Run the `get-credentials` command from the previous step to configure `kubectl`.

```bash
az aks get-credentials --resource-group <RESOURCE_GROUP> --name <CLUSTER_NAME>
```

> **Note:** `make helm-aks-deploy` (Part 4) checks that the current kube context contains the word `daedalus`. If your cluster name does not contain it, the Makefile will abort. Rename the context with `kubectl config rename-context <old> <new-name-with-daedalus>` if needed.

**5.** Verify KEDA is running in the cluster.

```bash
kubectl get pods -n kube-system -l app=keda-operator
```

Expected: one `keda-operator` pod in `Running` state.

---

## Part 2: Build and Push Images (~5 min)

> **Note:** `ACR_LOGIN_SERVER` is the value from Step 3 (e.g. `daedalustest.azurecr.io`). Set it as a shell variable for convenience:
> ```bash
> ACR_LOGIN_SERVER=$(cd deploy/terraform/ && terraform output -raw acr_login_server)
> ```

**6.** Authenticate Docker with ACR.

```bash
az acr login --name $(cd deploy/terraform/ && terraform output -raw acr_name)
```

**7.** Build and push `daedalus-proxy` from the project root.

```bash
docker build -f deploy/docker/Dockerfile.proxy -t "${ACR_LOGIN_SERVER}/daedalus-proxy:latest" .
docker push "${ACR_LOGIN_SERVER}/daedalus-proxy:latest"
```

**8.** Pull `copilot-bridge` from GHCR, re-tag for ACR, and push.

```bash
docker pull ghcr.io/raykao/copilot-bridge:latest
docker tag ghcr.io/raykao/copilot-bridge:latest "${ACR_LOGIN_SERVER}/copilot-bridge:latest"
docker push "${ACR_LOGIN_SERVER}/copilot-bridge:latest"
```

---

## Part 3: Create Kubernetes Secrets (~3 min)

**9.** Create the `daedalus` namespace.

```bash
kubectl create namespace daedalus
```

**10.** Create the ACR image pull secret using a service principal (or your own credentials).

```bash
kubectl create secret docker-registry acr-pull-secret \
  --docker-server=<ACR_LOGIN_SERVER> \
  --docker-username=<SP_CLIENT_ID> \
  --docker-password=<SP_CLIENT_SECRET> \
  --namespace daedalus
```

> **Note:** To create a service principal scoped to the ACR:
> ```bash
> ACR_ID=$(az acr show --name <ACR_NAME> --query id -o tsv)
> az ad sp create-for-rbac --name daedalus-acr-pull \
>   --role AcrPull \
>   --scopes "${ACR_ID}"
> ```
> Use the returned `appId` as `SP_CLIENT_ID` and `password` as `SP_CLIENT_SECRET`.

**11.** Create the GitHub token secret for the Copilot agent.

```bash
kubectl create secret generic copilot-secret \
  --from-literal=github-token=<GITHUB_TOKEN> \
  --namespace daedalus
```

---

## Part 4: Deploy with Helm (~5 min)

**12.** Deploy (or upgrade) the Daedalus Helm chart using the AKS overlay.

```bash
make helm-aks-deploy NAMESPACE=daedalus RELEASE_NAME=daedalus
```

This applies `deploy/helm/values-aks.yaml` on top of the chart defaults and waits up to 5 minutes for all resources to become ready.

> **Note:** The Makefile enforces that `kubectl config current-context` contains the string `daedalus`. If it does not, the deploy is aborted. See Step 4 for how to rename the context.

**13.** Verify the NATS pod is running.

```bash
kubectl get pods -n daedalus -l app.kubernetes.io/name=nats
```

**14.** Verify the KEDA ScaledJob was created.

```bash
kubectl get scaledjobs -n daedalus
```

Expected: one ScaledJob named `daedalus-copilot` (or similar) in `Ready` state.

---

## Part 5: Validate (~7 min)

**15.** Run the end-to-end validation script. It creates the JetStream stream, publishes a test task, waits for a Job pod to start, measures cold-start latency, and validates graceful shutdown.

```bash
RELEASE_NAME=daedalus NAMESPACE=daedalus \
  bash test/scripts/validate-aks-deployment.sh
```

**16.** Review the timing output printed at the end of the script. Compare against the expected values documented in [AKS Deployment Guide - Expected Behaviors](aks-deployment.md#expected-behaviors).

---

## Teardown

Remove the Helm release (leaves the namespace and manually created secrets in place):

```bash
make helm-aks-teardown NAMESPACE=daedalus RELEASE_NAME=daedalus
```

Destroy all Azure infrastructure (AKS cluster, ACR, resource group):

```bash
cd deploy/terraform/ && make destroy
```

> **Note:** `make helm-aks-teardown` does NOT delete the `daedalus` namespace or the secrets you created in Steps 10-11. To remove everything: `kubectl delete namespace daedalus`.

---

## Quick Reference

| Command | Purpose |
|---------|---------|
| `make helm-aks-deploy` | Install or upgrade Helm release on AKS |
| `make helm-aks-teardown` | Remove Helm release (keeps namespace and secrets) |
| `make helm-aks-status` | Show Helm release status, ScaledJobs, Jobs, and Pods |
| `make helm-aks-logs` | Tail proxy + agent logs for the most recent pod (default: copilot) |
| `make helm-aks-logs WORKER=claude` | Tail logs for a specific worker |
| `kubectl get scaledjobs -n daedalus` | List KEDA ScaledJobs |
| `kubectl get jobs -n daedalus` | List active and completed Jobs |
| `kubectl get pods -n daedalus -o wide` | List all pods with node placement |
| `kubectl logs -n daedalus <pod> -c proxy` | Stream proxy sidecar logs |
| `kubectl logs -n daedalus <pod> -c agent` | Stream agent container logs |
| `kubectl describe scaledjob -n daedalus <name>` | Show ScaledJob events and trigger config |
| `terraform output` | Print all Terraform outputs (ACR server, credentials command) |
| `cd deploy/terraform/ && make deploy` | Provision AKS, ACR, KEDA (two-phase apply) |
| `cd deploy/terraform/ && make destroy` | Tear down all Azure resources |

---

> Full configuration reference, troubleshooting steps, and expected behavior details: [AKS Deployment Guide](aks-deployment.md)
