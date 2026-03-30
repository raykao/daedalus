# Dark Software Factory: Industrializing Software Development with Autonomous Agent Orchestration

## Mission & Objective

Design and architect a **Dark Software Factory** - a fully autonomous, elastically scalable software development platform that orchestrates multiple GitHub Copilot agent instances as ephemeral microservices. Inspired by "dark factories" in manufacturing (fully automated facilities that operate without human presence on the floor), the goal is to industrialize software development by treating AI coding agents as containerized workers that spin up on demand, execute discrete tasks, and produce verifiable outputs - all orchestrated through message queues, Kubernetes, and Git-native workflows.

The factory should:

1. **Receive work** via message queues (issues, PR reviews, DevOps tasks, feature requests)
2. **Dispatch specialized agents** (e.g., `devops.agent.md`, `frontend.agent.md`, `security.agent.md`) as isolated containers
3. **Scale elastically** using KEDA (Kubernetes Event-Driven Autoscaling) based on queue depth
4. **Produce outputs** - code commits, PRs, review comments, documentation, infrastructure changes - pushed to Git repositories
5. **Persist reasoning** - store agent thoughts, decisions, and intermediate artifacts for auditability

---

## Core Concept

### The Manufacturing Analogy

| Manufacturing Concept | Software Factory Equivalent |
|---|---|
| Raw materials | Issues, feature requests, bug reports, queue messages |
| Assembly line stations | Specialized agents (devops, frontend, backend, security, docs) |
| Robots/machines | GitHub Copilot instances running in containers |
| Work orders | Messages on a queue (Azure Service Bus, RabbitMQ, NATS, etc.) |
| Quality inspection | Automated testing, code review agents, CI/CD pipelines |
| Finished goods | Merged PRs, deployed services, published artifacts |
| Factory floor | Kubernetes cluster |
| Shift management | KEDA autoscaler - scale-to-zero and back |
| Bill of materials | Agent definition files (`.github/agents/*.agent.md`) |

### Key Design Principles

- **Agents as Microservices**: Each agent type is a stateless, containerized service that can be independently scaled, deployed, and versioned.
- **Event-Driven Activation**: Agents are triggered by messages on queues - not polling, not cron. KEDA watches queue depth and scales agent deployments from zero to N.
- **Git as the Coordination Layer**: All code output flows through Git branches and PRs. Git is the source of truth, the collaboration protocol, and the audit trail.
- **Isolation by Default**: Each agent instance operates in its own filesystem, its own branch (or worktree), preventing conflicts between parallel workers.
- **Ephemeral Execution**: Agents spin up, do work, push results, and terminate. No long-lived processes holding state.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                        EVENT SOURCES                            │
│  GitHub Issues │ PR Events │ Webhooks │ Scheduled │ Manual CLI  │
└──────────┬──────────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────────────┐
│                      MESSAGE QUEUE                              │
│         (Azure Service Bus / RabbitMQ / NATS / Redis)           │
│                                                                 │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐       │
│  │ devops   │  │ frontend │  │ backend  │  │ security │       │
│  │ queue    │  │ queue    │  │ queue    │  │ queue    │       │
│  └──────────┘  └──────────┘  └──────────┘  └──────────┘       │
└──────────┬──────────────────────────────────────────────────────┘
           │  KEDA watches queue depth
           ▼
┌─────────────────────────────────────────────────────────────────┐
│                    KUBERNETES CLUSTER                           │
│                                                                 │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐  │
│  │ Agent Pod       │  │ Agent Pod       │  │ Agent Pod       │  │
│  │ ┌─────────────┐ │  │ ┌─────────────┐ │  │ ┌─────────────┐ │  │
│  │ │ Copilot CLI │ │  │ │ Copilot CLI │ │  │ │ Copilot CLI │ │  │
│  │ │ + GH CLI    │ │  │ │ + GH CLI    │ │  │ │ + GH CLI    │ │  │
│  │ │ + Git       │ │  │ │ + Git       │ │  │ │ + Git       │ │  │
│  │ └─────────────┘ │  │ └─────────────┘ │  │ └─────────────┘ │  │
│  │ Agent: devops   │  │ Agent: frontend │  │ Agent: security │  │
│  │ Branch: agent/  │  │ Branch: agent/  │  │ Branch: agent/  │  │
│  │  devops-<id>    │  │  fe-<id>        │  │  sec-<id>       │  │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘  │
│                                                                 │
│  KEDA ScaledObject: minReplicas=0, maxReplicas=N                │
└──────────┬──────────────────────────────────────────────────────┘
           │
           ▼
┌─────────────────────────────────────────────────────────────────┐
│                        OUTPUTS                                  │
│                                                                 │
│  Git Branches & PRs │ Blob/Object Storage │ Response Queues     │
│  (code changes)     │ (thoughts, logs,    │ (status updates,    │
│                     │  artifacts)         │  completions)       │
└─────────────────────────────────────────────────────────────────┘
```

---

## Component Deep Dive

### 1. Agent Definition Files (`.github/agents/*.agent.md`)

Each agent is defined by a markdown file that specifies its role, capabilities, system prompt, and tool access. These files serve as the "blueprint" for each type of worker in the factory.

**Current agents in this repo:**
- `speckit.analyze.agent.md`
- `speckit.clarify.agent.md`
- `speckit.implement.agent.md`
- `speckit.plan.agent.md`
- `speckit.specify.agent.md`
- `speckit.tasks.agent.md`
- `speckit.taskstoissues.agent.md`

**Proposed factory agents (examples):**
- `devops.agent.md` - Infrastructure, CI/CD, Terraform, Helm
- `frontend.agent.md` - UI components, styling, accessibility
- `backend.agent.md` - APIs, business logic, data models
- `security.agent.md` - Vulnerability scanning, dependency audit, secrets detection
- `reviewer.agent.md` - Code review, standards enforcement
- `docs.agent.md` - Documentation generation, README updates
- `triage.agent.md` - Issue classification, routing to appropriate queues

### 2. GitHub Copilot CLI & SDK - The Agent Runtime (Research Update 2026-03-05)

> **Status**: This section resolves follow-up item #1 ("Copilot CLI headless execution") and partially addresses #2 ("GitHub App authentication for agents").

There are **three complementary runtime options** for driving agents in the dark factory. Each operates at a different level of abstraction and suits different orchestration patterns.

#### Option A: Copilot CLI - Standalone Headless Agent (Primary)

The GitHub Copilot CLI (`@github/copilot`) is a standalone npm package (GA as of Feb 2026) that runs as a full agentic coding assistant from the terminal. It is the **primary candidate** for the dark factory's agent runtime.

**Installation:**
```bash
npm install -g @github/copilot
```
**Prerequisites:** Node.js v22+, npm v10+, active Copilot subscription (Pro/Business/Enterprise).

**Headless / Non-Interactive Execution:**

The CLI supports single-shot, non-interactive mode via the `--prompt` (`-p`) flag - critical for containerized automation:

```bash
# Single-shot: run prompt, produce output, exit
copilot -p "Refactor the auth module to use JWT tokens" \
  --model claude-sonnet-4 \
  --allow-all-tools \
  --allow-all-paths \
  --yolo
```

**Key CLI Flags for Automation:**

| Flag | Purpose | Security Notes |
|---|---|---|
| `-p` / `--prompt` | Single-shot non-interactive prompt | Required for headless |
| `--model <model>` | Select LLM (e.g., `claude-opus-4.6`, `gpt-5.3-codex`) | Match to task complexity |
| `--yolo` | Auto-approve all actions without confirmation | ⚠️ Use with scoped permissions |
| `--allow-all-tools` | Auto-approve all tool usage | ⚠️ Scope down in production |
| `--allow-all-paths` | Grant full filesystem access | ⚠️ Use with container isolation |
| `--allow-tool <glob>` | Whitelist specific tools | ✅ Preferred for production |
| `--deny-tool <glob>` | Blacklist specific tools | ✅ Defense in depth |
| `--timeout <seconds>` | Maximum execution time | ✅ Essential for cost control |
| `-s` / `--stdout-only` | Output only the response (no metadata) | Useful for piping output |

**Authentication for Headless Containers:**

```bash
# Set token as environment variable - CLI picks it up automatically
export GITHUB_TOKEN=ghp_xxxxxxxxxxxx
# or
export GH_TOKEN=ghp_xxxxxxxxxxxx

# For GitHub Enterprise:
export GH_HOST=github.yourcompany.com
```

Token requirements:
- Must have **"Copilot Requests"** permission scope
- For factory use: GitHub App installation tokens (short-lived, scoped) are preferred over long-lived PATs
- Alternative: `export GITHUB_ASKPASS=/path/to/script` for custom credential providers (e.g., Kubernetes secrets, vault integration)

#### Option B: Copilot SDK - Programmatic Agent Sessions (Advanced)

The [GitHub Copilot SDK](https://github.com/github/copilot-sdk) (`@github/copilot-sdk` on npm, `github-copilot-sdk` on PyPI) provides language-specific clients for deep programmatic control over agent sessions.

**Architecture:**
```
┌──────────────────┐     JSON-RPC 2.0      ┌──────────────────┐
│  Your Application │ ◄──── stdio/TCP ────► │  Copilot CLI      │
│  (SDK Client)     │                       │  (Server Mode)    │
│  TypeScript/Python │                       │  --server --stdio │
│  Go / .NET        │                       │                   │
└──────────────────┘                       └──────────────────┘
```

- SDK clients spawn the Copilot CLI in **server mode** (`--server --stdio`) and communicate via **JSON-RPC 2.0** over stdio (default) or TCP (`--headless --port N`).
- All orchestration logic lives in the CLI binary; SDKs are thin wrappers ensuring cross-language consistency.

**Session Lifecycle (TypeScript example):**

```typescript
import { CopilotClient } from "@github/copilot-sdk";

const client = new CopilotClient();
await client.start();

// Create an agent session with model selection
const session = await client.createSession({ model: "claude-sonnet-4" });

// Event-driven response handling
session.on("assistant.message", (event) => {
  console.log(event.data.content);
});

// Send task prompt
await session.send({
  prompt: "Implement the KEDA ScaledJob manifest for the devops-agent queue"
});

// Clean up
await session.destroy();
await client.stop();
```

**SDK Capabilities relevant to the Dark Factory:**
- **Multi-session management**: Run multiple concurrent agent conversations (one per task)
- **Model routing**: Dynamically select models per task (opus for complex reasoning, haiku for simple changes)
- **Streaming events**: Real-time progress monitoring for observability
- **MCP tool integration**: Extend agent capabilities with custom Model Context Protocol tools
- **Session persistence**: Resume interrupted agent sessions (useful for retry/recovery)

**SDK Languages:** TypeScript/Node.js, Python, Go, .NET

**When to use SDK vs CLI:**

| Use Case | CLI (`-p` flag) | SDK (sessions) |
|---|---|---|
| Simple one-shot tasks | ✅ Preferred | Overkill |
| Multi-step workflows | Possible but fragile | ✅ Preferred |
| Real-time progress monitoring | Limited | ✅ Streaming events |
| Custom tool integration | Via `--allow-tool` | ✅ Full MCP support |
| Multiple concurrent agents | Multiple processes | ✅ Multi-session |
| Container entrypoint | ✅ Simple | More complex setup |

#### Option C: Copilot Coding Agent - GitHub-Hosted (Alternative Path)

GitHub's own Copilot Coding Agent is a managed service where you assign issues to `@copilot` and it autonomously creates branches, writes code, runs tests, and opens PRs - all in ephemeral GitHub Actions containers.

**How it works:**
1. Assign an issue to `@copilot` (UI, CLI, or API)
2. Copilot spins up an isolated Actions runner container
3. Reads `.github/agents/*.agent.md` for custom behavior
4. Uses `.github/workflows/copilot-setup-steps.yml` for environment setup
5. Creates branch, writes code, runs CI, opens PR
6. Human reviews and merges (agent cannot self-merge)

**Programmatic Assignment via GraphQL API:**

```graphql
# Step 1: Get Copilot's actor ID
query($owner: String!, $name: String!) {
  repository(owner: $owner, name: $name) {
    suggestedActors(capabilities: [CAN_BE_ASSIGNED], first: 100) {
      nodes {
        login
        ... on Bot { id }
      }
    }
  }
}

# Step 2: Assign issue to Copilot
mutation($assignableId: ID!, $actorIds: [ID!]!) {
  addAssigneesToAssignable(input: {
    assignableId: $assignableId,
    assigneeIds: $actorIds
  }) {
    assignable {
      ... on Issue {
        id
        number
        assignees(first: 10) { nodes { login } }
      }
    }
  }
}
```

**Required header:** `GraphQL-Features: issues_copilot_assignment_api_support`

**Environment customization** (`.github/workflows/copilot-setup-steps.yml`):

```yaml
name: "Copilot Setup Steps"
on: workflow_dispatch

jobs:
  copilot-setup-steps:
    runs-on: ubuntu-latest
    container:
      image: myorg/custom-dev-image:latest  # Optional custom container
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with: { node-version: '22' }
      - run: npm ci
```

**Relevance to Dark Factory:**

| Aspect | Self-Hosted (CLI/SDK in K8s) | GitHub-Hosted (Coding Agent) |
|---|---|---|
| Scaling control | Full (KEDA, custom pods) | Limited (GitHub's infra) |
| Queue integration | Any (Service Bus, NATS, etc.) | GitHub Issues only |
| Custom tooling | Unlimited | Limited to Actions ecosystem |
| Cost model | Compute + API calls | Per-agent-minute pricing |
| Multi-repo orchestration | Custom orchestrator | Per-repo, manual or API |
| Isolation | Container/K8s namespace | GitHub Actions runner |
| Merge policy | Configurable | Human-only (by design) |
| Setup complexity | High | Low |

**Recommendation:** Use the **Copilot Coding Agent** for simple, single-repo tasks that fit the issue→PR workflow. Use the **self-hosted CLI/SDK approach** for the full dark factory with custom queues, multi-agent orchestration, and elastic scaling.

#### Hybrid Architecture

The most pragmatic approach combines both:

```
┌────────────────────────────────────────────────────────────────┐
│                    DARK FACTORY ORCHESTRATOR                    │
├────────────────────────────────────────────────────────────────┤
│                                                                │
│  Simple tasks ──► GitHub Coding Agent (assign @copilot)        │
│  (single-repo,     └── Uses .github/agents/*.agent.md         │
│   standard flow)    └── Runs in GitHub Actions                 │
│                                                                │
│  Complex tasks ──► Self-Hosted Agent Pods (K8s + KEDA)        │
│  (multi-repo,       └── Copilot CLI/SDK in containers         │
│   custom queues,    └── Custom tool access via MCP             │
│   chained agents)   └── Full orchestration control             │
│                                                                │
└────────────────────────────────────────────────────────────────┘
```

### 3. Container Image (Updated)

A production-ready container image for self-hosted agents:

```dockerfile
FROM node:22-alpine

# Core tooling
RUN apk add --no-cache git curl jq bash openssh-client

# Install GitHub Copilot CLI
RUN npm install -g @github/copilot

# Install GitHub CLI (for PR creation, issue management)
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | \
    dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] \
    https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list && \
    apk add --no-cache github-cli || npm install -g @github/cli

# Agent entrypoint script
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
```

**Entrypoint script** (`entrypoint.sh`):

```bash
#!/bin/bash
set -euo pipefail

# --- Configuration from environment/queue message ---
REPO="${AGENT_REPO:?Missing AGENT_REPO}"
AGENT_TYPE="${AGENT_TYPE:?Missing AGENT_TYPE}"
TASK_PROMPT="${AGENT_TASK_PROMPT:?Missing AGENT_TASK_PROMPT}"
RUN_ID="${AGENT_RUN_ID:-$(uuidgen)}"
BRANCH="agent/${AGENT_TYPE}-${RUN_ID}"
MODEL="${AGENT_MODEL:-claude-sonnet-4}"
TIMEOUT="${AGENT_TIMEOUT:-600}"

# --- Clone & branch ---
git clone --depth=1 "https://x-access-token:${GITHUB_TOKEN}@github.com/${REPO}.git" /workspace
cd /workspace
git checkout -b "${BRANCH}"

# --- Execute agent ---
copilot -p "${TASK_PROMPT}" \
  --model "${MODEL}" \
  --timeout "${TIMEOUT}" \
  --allow-all-tools \
  --allow-all-paths \
  --yolo

# --- Commit & push ---
git add -A
if git diff --cached --quiet; then
  echo "No changes to commit"
else
  git commit -m "agent(${AGENT_TYPE}): ${AGENT_TASK_PROMPT:0:72}

Run-ID: ${RUN_ID}
Agent: ${AGENT_TYPE}
Model: ${MODEL}

Co-authored-by: Copilot <223556219+Copilot@users.noreply.github.com>"
  git push origin "${BRANCH}"

  # --- Create PR ---
  gh pr create \
    --title "agent(${AGENT_TYPE}): ${AGENT_TASK_PROMPT:0:72}" \
    --body "Automated PR from Dark Factory agent run.

**Agent:** ${AGENT_TYPE}
**Run ID:** ${RUN_ID}
**Model:** ${MODEL}
**Prompt:** ${AGENT_TASK_PROMPT}" \
    --base main \
    --head "${BRANCH}"
fi

echo "Agent run complete: ${RUN_ID}"
```

The entrypoint script:
1. Reads configuration from environment variables (injected by queue consumer/K8s)
2. Clones the target repository (shallow clone for speed)
3. Creates a unique branch (`agent/<agent-type>-<run-id>`)
4. Invokes the Copilot CLI in single-shot mode with the task prompt
5. Commits and pushes changes
6. Creates a PR via GitHub CLI
7. Exits (pod terminates, KEDA scales down)

### 4. Secrets & Credential Management (Research Update 2026-03-05)

A critical design concern for the dark factory is how agents obtain credentials - not just for GitHub (Copilot API, repo access) but for any external service an agent task may require (cloud APIs, databases, artifact registries, SaaS integrations). The design must enforce **least privilege**, **ephemerality**, **auditability**, and support **human-in-the-loop approval** where appropriate.

#### Design Principles

1. **Never statically inject secrets at container build time** - secrets must be resolved at runtime, scoped to the specific agent session.
2. **Agents should not know secret values directly** - they should request access by name/reference, and the secret store resolves the value.
3. **Orchestrator controls visibility** - the parent/orchestrator agent can see what secret *names* are available (metadata) to dynamically determine which credentials a subordinate agent needs, without seeing the values themselves.
4. **Human-in-the-loop for sensitive operations** - certain secret categories (production credentials, payment APIs, infrastructure admin) should require human approval before being dispensed to an agent.
5. **Short-lived, auto-rotating tokens** - prefer ephemeral tokens that expire with the agent's session over long-lived credentials.
6. **Audit trail** - every secret access by an agent must be logged with agent type, run ID, timestamp, and the secret name (never the value).

#### Architecture: Three-Layer Secret Access

```
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 1: SECRET STORES (Source of Truth)                       │
│                                                                 │
│  ┌──────────────┐  ┌──────────────┐  ┌────────────────────┐   │
│  │ Azure Key    │  │ 1Password    │  │ HashiCorp Vault    │   │
│  │ Vault        │  │ (Connect     │  │                    │   │
│  │              │  │  Server)     │  │                    │   │
│  └──────┬───────┘  └──────┬───────┘  └────────┬───────────┘   │
│         │                 │                    │               │
└─────────┼─────────────────┼────────────────────┼───────────────┘
          │                 │                    │
          ▼                 ▼                    ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 2: KUBERNETES SECRET INTEGRATION                         │
│                                                                 │
│  Option A: External Secrets Operator (ESO)                      │
│    - Syncs secrets into K8s Secret objects                       │
│    - Works with env vars, volumes, existing tooling             │
│    - Secrets stored in etcd (encrypted at rest)                 │
│                                                                 │
│  Option B: Secrets Store CSI Driver                             │
│    - Mounts secrets as tmpfs volumes (never in etcd)            │
│    - Higher security, secrets are ephemeral in-memory only      │
│    - Requires apps to read from mounted files                   │
│                                                                 │
│  Option C: 1Password Kubernetes Injector                        │
│    - Mutating webhook injects secrets at pod start              │
│    - No persistent K8s Secret created                           │
│    - Just-in-time resolution via Connect Server                 │
│                                                                 │
└──────────────────────────┬──────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────┐
│  LAYER 3: AGENT RUNTIME                                         │
│                                                                 │
│  Agent pod reads secrets via:                                   │
│    - Environment variables (GITHUB_TOKEN, API_KEY, etc.)        │
│    - Mounted files (/secrets/github-token, etc.)                │
│    - CLI tools (op read, az keyvault secret show, vault kv get) │
│                                                                 │
│  Agent NEVER sees secret metadata catalog - only the            │
│  orchestrator has that visibility.                              │
└─────────────────────────────────────────────────────────────────┘
```

#### Option Comparison

| Feature | Azure Key Vault + Workload Identity | 1Password Connect + Operator | HashiCorp Vault |
|---|---|---|---|
| **Dev Friendliness** | Medium - Azure-native | High - familiar UX, CLI (`op read`) | Medium - powerful but complex |
| **K8s Integration** | CSI Driver or ESO | Operator, Injector, or ESO | CSI Driver, ESO, or Agent Sidecar |
| **Auth Model** | OIDC Workload Identity (keyless) | Service Account Token | AppRole, K8s Auth, OIDC |
| **Human Approval** | Via Azure RBAC / PIM | Via 1Password access policies | Via Sentinel policies |
| **Secret Rotation** | Automatic | Automatic (Operator restarts pods) | Automatic (dynamic secrets) |
| **Audit Trail** | Azure Monitor / Diagnostic Logs | 1Password audit log | Vault audit log |
| **Multi-cloud** | Azure only | Cloud-agnostic | Cloud-agnostic |
| **Cost** | Included with Azure | 1Password Business/Enterprise | Open source (self-hosted) or HCP |
| **Local Dev** | Azure CLI (`az keyvault`) | 1Password CLI (`op read`) | Vault CLI (`vault kv get`) |

#### 1Password: The Developer-Friendly Path

1Password stands out for the dark factory because of its **developer-centric CLI** and **secret reference** pattern:

**Secret references** - store references in code/config, resolve at runtime:
```bash
# In agent configuration or .env file (committed safely):
GITHUB_TOKEN=op://Factory/github-copilot-token/credential
DATABASE_URL=op://Factory/postgres-prod/connection-string
AZURE_CLIENT_SECRET=op://Factory/azure-sp/client-secret

# Agent entrypoint resolves all references before execution:
op run -- copilot -p "${TASK_PROMPT}" --yolo --allow-all-tools
```

`op run` replaces all `op://` references with actual values in the process environment, and the values exist **only in memory for that process** - never written to disk.

**Orchestrator metadata visibility** - the parent agent can list available secrets without reading values:
```bash
# List available items in a vault (names/metadata only, no values)
op item list --vault Factory --format json | jq '.[].title'
# Output: ["github-copilot-token", "postgres-prod", "azure-sp", ...]

# The orchestrator can now decide which secrets a subordinate needs
# and scope the agent's service account to only those items
```

**1Password Kubernetes Operator** for automated sync:
```yaml
apiVersion: onepassword.com/v1
kind: OnePasswordItem
metadata:
  name: github-copilot-token
  namespace: dark-factory
spec:
  itemPath: "vaults/Factory/items/github-copilot-token"
```

**1Password Kubernetes Injector** for just-in-time injection (no persistent K8s Secret):
- Mutating admission webhook intercepts pod creation
- Resolves `op://` references in env vars via Connect Server
- Injects real values into the pod's environment at startup
- Values never stored in etcd or Kubernetes API

#### Azure Key Vault: The Enterprise Path

For Azure-native environments, Azure Key Vault with **Workload Identity** provides keyless, OIDC-federated authentication:

```
GitHub App (OIDC) → Azure AD Workload Identity → Managed Identity → Key Vault
     │                                                                    │
     └── Short-lived JWT ──► exchanged for ──► scoped Azure token ──► secret access
```

**Setup flow:**
1. Enable OIDC + Workload Identity add-on on AKS cluster
2. Create user-assigned managed identity with Key Vault Secrets User role
3. Create K8s service account federated to the managed identity
4. Use CSI Driver or ESO to mount secrets into agent pods
5. No static credentials anywhere - authentication is entirely OIDC-based

```yaml
# SecretProviderClass for CSI Driver
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: agent-secrets
spec:
  provider: azure
  parameters:
    usePodIdentity: "false"
    useVMIdentity: "false"
    clientID: "<managed-identity-client-id>"
    keyvaultName: "dark-factory-vault"
    objects: |
      array:
        - |
          objectName: github-copilot-token
          objectType: secret
        - |
          objectName: agent-api-key
          objectType: secret
    tenantId: "<tenant-id>"
```

#### Human-in-the-Loop Approval Patterns

For sensitive operations, the factory should support gated secret access:

```
Agent requests credential ──► Orchestrator evaluates policy
                                    │
                    ┌────────────────┼────────────────┐
                    ▼                ▼                ▼
              AUTO-APPROVE      QUEUE FOR          DENY
              (low risk)        HUMAN REVIEW      (policy violation)
              - Read-only       - Prod DB write    - Admin creds
                API keys        - Payment APIs     - Root access
              - Dev/staging     - Infra changes    - Cross-tenant
                tokens
```

**Implementation approaches:**
1. **Vault Sentinel Policies** - HashiCorp Vault's policy engine can require multi-party approval for certain secret paths
2. **1Password Access Policies** - vault-level access controls with approval workflows
3. **Azure PIM (Privileged Identity Management)** - just-in-time, time-bound elevated access with approval chains
4. **Custom Orchestrator Logic** - the factory's orchestrator service checks a policy engine before provisioning secrets to an agent pod's namespace

#### Recommended Architecture for Dark Factory

**For development/prototyping:**
- **1Password CLI + Connect Server** - fastest to set up, best developer UX, `op run` pattern maps perfectly to agent entrypoints

**For production/enterprise:**
- **Azure Key Vault + Workload Identity + CSI Driver** (Azure environments) or **HashiCorp Vault + K8s Auth** (multi-cloud)
- **External Secrets Operator** as a common abstraction layer that supports all backends
- **1Password** as the developer-facing secret management UX that feeds into the enterprise store

**Hybrid pattern:**
```yaml
# External Secrets Operator can pull from multiple backends simultaneously
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: factory-1password
spec:
  provider:
    onepassword:
      connectHost: http://onepassword-connect:8080
      vaults:
        Factory: 1
      auth:
        secretRef:
          connectTokenSecretRef:
            name: op-connect-token
            key: token
---
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: factory-azure-kv
spec:
  provider:
    azurekv:
      authType: WorkloadIdentity
      vaultUrl: "https://dark-factory-vault.vault.azure.net"
      serviceAccountRef:
        name: factory-workload-identity
```

This allows the orchestrator to source secrets from whichever backend owns them - 1Password for developer-managed secrets, Azure Key Vault for infrastructure secrets, Vault for dynamic database credentials - all through a unified `ExternalSecret` interface.

### 5. Git Isolation Strategy

**Single-agent per filesystem (simple, default):**
- Each agent pod clones the repo fresh
- Creates its own branch: `agent/<type>-<uuid>`
- Full isolation, no conflicts
- Higher clone overhead (mitigable with shallow clones + sparse checkout)

**Multi-agent shared filesystem (advanced, worktrees):**
- A shared PVC holds the main repo clone
- Each agent creates a `git worktree` for its branch
- Lower disk/network overhead
- Requires coordination to avoid worktree conflicts
- Best suited for a single node with multiple agent pods

**Decision Matrix:**

| Scenario | Strategy | Reason |
|---|---|---|
| Cloud/multi-node K8s | Branch per pod (clone) | Simple, fully isolated, horizontally scalable |
| Single machine / local dev | Git worktrees | Efficient disk usage, fast checkout |
| Monorepo with many agents | Sparse checkout + branch | Only pull needed paths |

### 4. KEDA Integration

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: devops-agent-scaler
spec:
  scaleTargetRef:
    name: devops-agent
  minReplicaCount: 0          # Scale to zero when idle
  maxReplicaCount: 10         # Cap concurrent agents
  pollingInterval: 10         # Check queue every 10s
  cooldownPeriod: 300         # Wait 5 min before scale-down
  triggers:
    - type: azure-servicebus
      metadata:
        queueName: devops-tasks
        messageCount: "1"     # 1 pod per message
        connectionFromEnv: SERVICE_BUS_CONNECTION
```

Each agent type gets its own `ScaledObject` mapping a queue to a Kubernetes Deployment. When a message arrives on `devops-tasks`, KEDA scales the `devops-agent` deployment from 0 → 1 (or more). When the queue drains, it scales back to zero.

### 5. Message Schema

A standardized message format for dispatching work to agents:

```json
{
  "id": "msg-uuid-here",
  "timestamp": "2026-03-04T23:50:00Z",
  "agent_type": "devops",
  "repository": "raykao/dark-factory",
  "ref": "main",
  "task": {
    "type": "issue",
    "source_id": "42",
    "title": "Add Helm chart for API service",
    "description": "Create a production-ready Helm chart...",
    "context": {
      "files": ["kubernetes/", "Dockerfile"],
      "related_issues": [38, 40]
    }
  },
  "output": {
    "branch_prefix": "agent/devops",
    "create_pr": true,
    "storage_path": "runs/msg-uuid-here/",
    "response_queue": "agent-results"
  },
  "constraints": {
    "max_runtime_seconds": 600,
    "model": "claude-sonnet-4",
    "tools": ["bash", "edit", "create", "grep", "glob"]
  }
}
```

### 6. Output & Persistence

| Output Type | Destination | Purpose |
|---|---|---|
| Code changes | Git branch → PR | Reviewable, mergeable artifacts |
| Agent thoughts/reasoning | Blob storage (S3/Azure Blob/GCS) | Auditability, debugging, learning |
| Status/completion | Response queue | Notify orchestrator, trigger downstream |
| Metrics | Prometheus/OpenTelemetry | Observability, cost tracking |
| Logs | Stdout → cluster logging (Fluentd/Loki) | Operational debugging |

---

## Knowns

- **GitHub Copilot CLI exists** as a standalone npm package (`@github/copilot`, GA Feb 2026) and can be invoked non-interactively via `copilot -p "prompt"` in headless containers. Requires Node.js v22+ and a Copilot subscription.
- **Copilot CLI supports headless auth** via `GITHUB_TOKEN` or `GH_TOKEN` environment variables. Tokens need "Copilot Requests" permission scope. Custom credential providers supported via `GITHUB_ASKPASS`.
- **Copilot CLI supports model selection** via `--model` flag, enabling per-task model routing (e.g., opus for complex reasoning, haiku for simple edits).
- **Copilot CLI supports tool permission scoping** via `--allow-tool`, `--deny-tool`, `--allow-all-tools`, and `--allow-all-paths` flags - critical for security in automated environments.
- **GitHub Copilot SDK** (`@github/copilot-sdk`) provides programmatic agent session management via JSON-RPC 2.0 over stdio/TCP, available in TypeScript, Python, Go, and .NET.
- **GitHub Copilot Coding Agent** is a managed service where issues assigned to `@copilot` are autonomously processed in ephemeral Actions containers. Can be automated via GraphQL API (`addAssigneesToAssignable` mutation with `GraphQL-Features: issues_copilot_assignment_api_support` header).
- **`copilot-setup-steps.yml`** workflow allows custom container images and environment setup for the hosted Coding Agent.
- **GitHub CLI (`gh`)** provides a rich SDK for repository operations (clone, branch, PR creation, issue management).
- **KEDA** is a mature, CNCF-graduated project that supports 60+ event sources (Azure Service Bus, RabbitMQ, Kafka, Redis, AWS SQS, etc.).
- **Git worktrees** allow multiple working directories from a single repository clone, enabling parallel work without conflicts.
- **Kubernetes Jobs** (vs Deployments) may be more appropriate for one-shot agent tasks - KEDA supports `ScaledJob` for this pattern.
- **Agent definition files** (`.github/agents/*.agent.md`) are already a pattern in this repository and can be extended.
- **Container-based isolation** is well-understood and provides strong process/filesystem/network boundaries.

## Unknowns

- ~~**Copilot CLI programmatic invocation**: Can `gh copilot` be invoked non-interactively in a headless container? What authentication flow is needed? Is there a REST/gRPC API, or is it purely CLI-based?~~ **RESOLVED** - Yes. `copilot -p "prompt" --yolo --allow-all-tools` runs non-interactively. Auth via `GITHUB_TOKEN` env var. SDK provides JSON-RPC API for programmatic sessions.
- ~~**Token/authentication management**: GitHub App installation tokens are the recommended path for ephemeral containers.~~ **RESOLVED** - Three-tier secret architecture designed: secret stores (1Password/Azure KV/Vault) → K8s integration layer (ESO/CSI/Injector) → agent runtime. 1Password `op run` for dev, Azure Workload Identity (OIDC, keyless) for enterprise. Human-in-the-loop patterns documented.
- **Agent context limits**: How much repository context can an agent effectively process? The CLI operates on the local filesystem - it can read files, but context window limits of the underlying LLM still apply. Sparse checkout may help for monorepos.
- **Inter-agent communication**: If Agent A's output feeds into Agent B's input, what's the coordination pattern? Direct queue chaining? Saga/orchestrator pattern?
- **Cost model**: What are the API costs of running N concurrent Copilot instances? Is there rate limiting? How does pricing scale? The Coding Agent has per-agent-minute pricing; CLI/SDK costs are based on Copilot subscription + model token usage.
- **Conflict resolution**: When two agents modify overlapping files on separate branches, who resolves merge conflicts? A dedicated "merge agent"?
- **Quality gates**: How do we prevent agents from merging bad code? CI must pass, but do we need a human-in-the-loop approval step? (The Coding Agent enforces human-only merge by design.)
- **CLI vs SDK reliability in containers**: How stable is the Copilot CLI in long-running container workloads? What happens on network interruption, token expiry, or LLM timeout?

## Gaps

1. ~~**No existing container image** for running Copilot CLI headlessly - needs to be built and tested.~~ **PARTIALLY RESOLVED** - Container image design and entrypoint script are now documented above. Base image: `node:22-alpine` + `@github/copilot` npm package. Needs actual build and test validation.
2. **No message schema standard** for dispatching work to agents - needs design and validation.
3. **No orchestrator component** - something needs to receive GitHub webhooks, classify work, and route to the correct queue. (Could be a simple GitHub Action, a dedicated service, or the `triage.agent.md` itself.)
4. **No observability stack** defined for tracking agent runs, costs, success rates, and cycle times.
5. **No security model** for agent permissions - principle of least privilege for each agent type (e.g., security agent shouldn't push to `main`). The CLI's `--allow-tool`/`--deny-tool` flags provide a starting point for tool-level RBAC.
6. **No feedback loop** - how do agents learn from rejected PRs, failed CI, or human review comments?
7. **No local development story** - how does a developer test/iterate on agent definitions without deploying to K8s? (The CLI can be tested locally with `copilot -p "prompt"` which helps.)
8. **No state management pattern** - for multi-step tasks that span multiple agent invocations. The Copilot SDK's session persistence may help here.
9. **No hybrid routing logic** - need decision framework for when to use GitHub-hosted Coding Agent vs self-hosted CLI/SDK agents.

---

## Proposed System Flow

```
1. Event occurs (issue created, PR opened, webhook fired, manual trigger)
         │
         ▼
2. Ingestion Service / GitHub Action receives event
         │
         ▼
3. Triage: Classify event → determine agent type(s) needed
         │
         ▼
4. Enqueue: Write structured message to agent-specific queue
         │
         ▼
5. KEDA detects message → scales agent pod from 0 → 1
         │
         ▼
6. Agent Pod starts:
   a. Read message from queue
   b. Clone repo (shallow) or attach to worktree
   c. Create working branch
   d. Load agent definition (.github/agents/<type>.agent.md)
   e. Execute Copilot CLI with task prompt
   f. Commit changes to branch
   g. Push branch, optionally create PR
   h. Write thoughts/reasoning to storage
   i. Write completion status to response queue
   j. Acknowledge message, exit
         │
         ▼
7. CI/CD runs on branch/PR (tests, linting, security scans)
         │
         ▼
8. Review: Human or reviewer-agent evaluates the PR
         │
         ▼
9. Merge or request changes (loop back to step 1 if changes requested)
         │
         ▼
10. Deploy (if applicable)
```

---

## Technology Stack (Proposed)

| Layer | Technology | Rationale |
|---|---|---|
| Container Runtime | Kubernetes (AKS/EKS/GKE) | Industry standard, KEDA-native |
| Autoscaler | KEDA | Event-driven scale-to-zero, broad trigger support |
| Message Queue | Azure Service Bus / NATS | Managed, reliable, KEDA-supported |
| Agent Runtime | GitHub Copilot CLI (`@github/copilot`) + Copilot SDK (`@github/copilot-sdk`) + GH CLI | Native GitHub integration, headless support, multi-model |
| Agent Runtime (Hosted) | GitHub Copilot Coding Agent | Managed alternative for simple tasks, assign via GraphQL API |
| Source Control | Git + GitHub | PR-based workflow, webhook ecosystem |
| Storage (artifacts) | Azure Blob / S3 / GCS | Cheap, durable, scalable |
| Observability | OpenTelemetry + Grafana | Vendor-neutral, comprehensive |
| Secrets Management | External Secrets Operator + 1Password Connect / Azure Key Vault / HashiCorp Vault | Multi-backend, runtime-resolved, never in code |
| CI/CD | GitHub Actions | Native to GitHub, runs on PR events |
| Local Dev | Docker Compose + kind/k3d | Lightweight local K8s for testing |

---

## Research Follow-ups

### Immediate (Required to validate feasibility)

1. **[✅ RESEARCHED] Copilot CLI headless execution** - Confirmed: `copilot -p "prompt" --yolo --allow-all-tools` runs non-interactively. Auth via `GITHUB_TOKEN` env var. SDK provides JSON-RPC sessions. See "GitHub Copilot CLI & SDK" section above. **Next step:** Build and test the container image prototype.
2. **[✅ RESEARCHED] GitHub App authentication & secrets management** - Comprehensive secrets architecture designed. Three-layer model: secret stores (Azure Key Vault / 1Password / Vault) → K8s integration (ESO / CSI Driver / 1Password Injector) → agent runtime. 1Password `op run` pattern recommended for dev; Azure Workload Identity for enterprise. Human-in-the-loop approval patterns documented. See "Secrets & Credential Management" section. **Next step:** Prototype 1Password Connect + `op run` in container image.
3. **[ ] KEDA + ScaledJob prototype** - Build a minimal KEDA ScaledJob that processes a message from a queue and runs a simple script. Validate scale-to-zero and activation latency.
4. **[ ] Container image prototype** - Build a minimal Docker image with `gh` CLI, git, and a mock agent entrypoint. Test clone → branch → commit → push → PR workflow.
5. **[ ] Git worktree vs. clone benchmarking** - Measure clone time, disk usage, and conflict risk for both strategies across repo sizes (small, medium, monorepo).

### Short-term (Design decisions)

6. **[ ] Message schema design** - Formalize the JSON schema for agent task messages. Consider extensibility, versioning, and backward compatibility.
7. **[ ] Inter-agent orchestration patterns** - Research saga pattern, choreography vs orchestration, and how to chain agent tasks (e.g., implement → review → merge).
8. **[ ] Security model design** - Define RBAC for agents: which agents can push to which branches, create PRs, merge, access secrets, etc.
9. **[ ] Observability architecture** - Design metrics, traces, and logging for agent runs. Define SLIs (success rate, cycle time, cost per task).
10. **[ ] Local development experience** - Design a `docker-compose.yml` or `kind` setup that lets developers test agent definitions locally.

### Medium-term (Build & iterate)

11. **[ ] Orchestrator service** - Build the ingestion/triage service that receives GitHub webhooks and routes to agent queues.
12. **[ ] Agent SDK/framework** - Abstract common patterns (clone, branch, commit, push, PR) into a reusable shell library or CLI wrapper.
13. **[ ] Feedback loop mechanism** - Design how agents incorporate review feedback (re-process PR review comments as new tasks).
14. **[ ] Multi-repo support** - Extend the factory to operate across multiple repositories with shared agent definitions.
15. **[ ] Cost tracking & budgeting** - Implement per-agent, per-repo cost tracking and configurable spending limits.

### Long-term (Advanced capabilities)

16. **[ ] Agent specialization & routing ML** - Use historical data to improve triage accuracy and agent selection.
17. **[ ] Self-improving agents** - Agents that update their own `.agent.md` definitions based on success/failure patterns.
18. **[ ] Multi-model support** - Route tasks to different LLM backends (Claude, GPT, Gemini) based on task type, cost, or quality requirements.
19. **[ ] Human-in-the-loop workflows** - Configurable approval gates, escalation paths, and collaborative agent-human sessions.
20. **[ ] Factory-of-factories** - Meta-orchestration across multiple dark factories for large enterprise environments.

---

## Open Questions for Discussion

1. Should agents be **Kubernetes Jobs** (one-shot, guaranteed completion) or **Deployments** (long-running, pull from queue)? KEDA supports both via `ScaledJob` and `ScaledObject`.
2. Should the factory use **GitHub Actions** as the orchestration layer (simpler, GitHub-native) or a **standalone service** (more flexible, portable)?
3. What is the **minimum viable factory**? A single agent type responding to a single queue, or a multi-agent setup from the start?
4. How should we handle **agent failures** - retry, dead-letter queue, human escalation, or all three?
5. Is there value in agents **reviewing each other's work** before human review, or does that just add latency?

---

## References & Prior Art

- **Dark Factory (Manufacturing)**: Fully automated factories operating without human workers on the floor (e.g., FANUC, Lights-Out Manufacturing)
- **KEDA**: [keda.sh](https://keda.sh) - Kubernetes Event-Driven Autoscaling
- **GitHub Copilot CLI**: [docs.github.com/copilot/cli](https://docs.github.com/en/copilot/how-tos/copilot-cli/cli-getting-started) - Standalone agentic CLI, GA Feb 2026. Install via `npm install -g @github/copilot`.
- **GitHub Copilot SDK**: [github.com/github/copilot-sdk](https://github.com/github/copilot-sdk) - Multi-language SDK for programmatic agent sessions via JSON-RPC
- **GitHub Copilot Coding Agent**: [docs.github.com/copilot/coding-agent](https://docs.github.com/copilot/how-tos/use-copilot-agents/coding-agent/assign-copilot-to-an-issue) - Managed agent that processes GitHub Issues into PRs
- **Copilot Coding Agent API Assignment**: [GitHub Changelog Dec 2025](https://github.blog/changelog/2025-12-03-assign-issues-to-copilot-using-the-api/) - GraphQL API for programmatic issue assignment
- **Copilot Setup Steps**: [docs.github.com/copilot/customize-agent-environment](https://docs.github.com/en/copilot/how-tos/use-copilot-agents/coding-agent/customize-the-agent-environment) - Custom container and environment configuration
- **GitHub CLI**: [cli.github.com](https://cli.github.com) - Command-line interface for GitHub
- **Copilot CLI Command Reference**: [docs.github.com/copilot/cli-command-reference](https://docs.github.com/en/copilot/reference/cli-command-reference) - Full flag and option documentation
- **Git Worktrees**: `git worktree` documentation for parallel working directories
- **Saga Pattern**: Distributed transaction pattern for multi-step workflows
- **12-Factor App**: Methodology for building scalable, maintainable services (applicable to agent design)
- **Building Agents with Copilot SDK**: [Microsoft Tech Community](https://techcommunity.microsoft.com/blog/azuredevcommunityblog/building-agents-with-github-copilot-sdk-a-practical-guide-to-automated-tech-upda/4488948) - Practical guide with automation examples
- **Automated Repo Maintenance with Copilot**: [Pamela Fox's Blog](http://blog.pamelafox.org/2025/07/automated-repo-maintenance-with-github.html) - Real-world multi-repo automation patterns
