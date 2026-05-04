# Phase 6 Options - Possible Paths Forward

> **Status: scoping doc, not a committed plan.** This file enumerates candidate
> directions for daedalus's next phase. Once the human picks an option, a
> `phase6-plan.md` and an epic draft will be authored following the
> [Phase 5 pattern](phase5-plan.md).

## Where We Are

v0.2.0 was tagged at the close of Phase 4. Phase 5 has now merged on `main`:
Terraform IaC under `deploy/terraform/`, an image build/publish workflow with
multi-arch and OIDC to ACR, the one-command `make deploy-aks-test` flow, an
`aks_e2e`-tagged Go harness that drives a real task through NATS on a freshly
provisioned cluster, a TTL-driven cleanup workflow, and a full rewrite of the
runbook and deployment guide. v0.3.0 will tag once the live-validation
acceptance criteria on epic #33 are checked off (engineer post-merge tasks:
`terraform apply`, dispatch the build/publish workflow, run the happy path on a
real cluster).

The platform today has: NATS JetStream, KEDA `ScaledJob` with scale-to-zero
and SIGTERM graceful shutdown, ACP-mode Copilot CLI behind the queue-to-A2A
proxy, dynamic agent registry, the [runtime contract](runtime-contract.md),
AKS deployment as a single Make target, and a TTL-bounded test cluster
managed by Terraform plus a scheduled cleanup workflow.

What it lacks: more than one agent type running in production, session
resurrection across pod restarts, a multi-replica orchestrator, K8s operator
CRDs, observability beyond the PR #30 overlay (Prometheus, Grafana, Tempo
installed but not deeply instrumented), and any production hardening past
Phase 5's explicit "test cluster" scope.

## Decision Framework

Before reading the options, a small framework for picking among them:

1. **Validation experiments first.** Small, time-bounded experiments that
   unblock other tracks deserve priority over feature work. They have high
   information yield per unit of effort.
2. **Trigger-gated deferred items only when their trigger fires.** The
   [Deferred (Pending Validation)](plan.md#deferred-pending-validation) table
   names a specific trigger for each item. Do not promote one until its
   trigger is actually met; the trigger is in the table for a reason.
3. **Production hardening is its own track.** Security, observability
   deepening, and HA should not be bundled with feature work. They have
   different reviewers, different testing strategies, and different rollback
   paths.
4. **New agent types are the lowest-risk way to validate the runtime
   contract.** The contract has only ever been exercised against one agent.
   A second agent shakes out abstraction bugs while staying inside the known
   shape of the system.

## Options

Each option uses the same sub-template: one-liner, maturity gate, scope
band (S/M/L; no time estimates, per workspace policy), strategic value,
risks, sub-task sketch, and prerequisites.

### Option A: Session Resurrection Validation Experiment

**One-liner:** Run the
[`resumeSession()` validation experiment](../research/hybrid-comms-architecture.md#deferred---validate-first-build-later)
exactly as described in the research doc, and let the result decide whether
to invest in Postgres archive plus snapshot hooks (R17) or simplify to
checkpoint-summary-injection.

**Maturity gate:** Directly the trigger for the deferred items
"Session snapshot/restore" ("CLI `resumeSession()` validation experiment
succeeds"), "Centralized session state store (Postgres)" ("Session restore
validated"), and "Context-aware resurrection (R18)" ("Centralized store
built"). All three are listed in the
[Deferred table in plan.md](plan.md#deferred-pending-validation), and R17
plus R18 in the
[risk register](../research/hybrid-comms-architecture.md#open-questions-and-risk-register)
are explicitly marked "Promote if CLI restoration experiment succeeds /
Promote if centralized store is built."

**Estimated scope:** S. This is a measurement experiment, not an
implementation phase. The procedure is six shell commands plus the
bridge `/resume <uuid>` invocation.

**Strategic value:** Unblocks roughly 40% of the Deferred (Pending
Validation) table in one shot. Either the CLI's session-store survives
backup-and-restore round-tripping (green light to design R17's Postgres
archive properly), or it does not (we know to simplify the entire
session-continuity track to checkpoint-summary-injection on a fresh
session, which removes Postgres, blob storage, and R18's decision tree
from the roadmap entirely).

**Risks:**
- The experiment itself is cheap, but a "works on my laptop" pass does
  not guarantee it works under containerized state directories with
  different inode and mtime behaviour. Document the test environment.
- A partial pass (resume succeeds but loses some turns) is the worst
  outcome to interpret. Pre-commit to a binary acceptance criterion
  before running: "all turns from the original session are visible to
  the resumed agent" or it is a fail.

**Concrete sub-tasks (sketch):**
- Reproduce the experiment locally with the bridge against Copilot CLI
  (the exact `tar` / `mv` / `/resume` sequence in
  [research § Deferred](../research/hybrid-comms-architecture.md#deferred---validate-first-build-later)).
- Repeat inside a container with a non-root user, ephemeral volume, and
  a restored `~/.copilot/` tree placed pre-start.
- Define the acceptance criterion (binary pass/fail) and capture a
  transcript of the resumed session.
- Write the result up as a short experiment report under `research/`,
  with a recommendation: promote R17 or simplify to
  checkpoint-summary-injection.
- File a follow-up issue against `docs/plan.md` with the recommendation
  so the deferred-items table can be updated.

**Prerequisites:** None beyond a local Copilot CLI install and a working
bridge.

### Option B: Add a Second Production Agent Type

**One-liner:** Add Claude Code (or `aider`, or Gemini CLI) as a second
runtime under the existing
[runtime contract](runtime-contract.md) and the
[agent registry](agent-registry.md) so the contract is validated against
real diversity, not just Copilot CLI.

**Maturity gate:** Reduces the gap to the K8s-operator trigger ("Factory
reaches 5+ agent types" - see
[plan.md Deferred table](plan.md#deferred-pending-validation)). One
addition is not enough to fire the trigger, but it moves the count from
1 to 2 and, more importantly, exercises the runtime contract under a
second implementation - which is the real point. If the contract is
right, the second agent slots in cleanly. If the contract has bugs, a
second agent surfaces them.

**Estimated scope:** M. Implementation lives mostly under the runtime
contract and the agent registry. The Helm chart needs a second
ScaledJob template instance; the e2e harness needs a second smoke
case.

**Strategic value:**
- Validates the runtime contract abstraction is real, not aspirational.
- Provides a second data point for cold-start, queue-depth, and SIGTERM
  behaviour - useful input to Option C.
- Unlocks "pick the agent for the task" routing in the orchestrator
  (currently a degenerate single-choice).

**Risks:**
- An agent that does not fit the runtime contract reveals a
  contract bug, not an agent bug. Surface that as a finding in the PR
  body and update the contract; do not paper over with adapter
  spaghetti.
- If the chosen agent's session model is fundamentally different (no
  `resumeSession()`-equivalent), it changes the calculus on Option A's
  conclusion. Document this in the agent's adapter notes.

**Concrete sub-tasks (sketch):**
- Pick the second agent. Recommendation: Claude Code, because its CLI
  semantics are closest to Copilot's ACP mode and the agent-registry
  already anticipates it.
- Implement the runtime adapter (queue-to-A2A proxy variant or direct
  ACP if the agent supports it).
- Add a `ScaledJob` template instance and Helm values block.
- Extend the e2e harness with a second happy-path test that publishes
  a task to the second agent's subject and asserts the artifact.
- Document the rollout in `docs/agent-registry.md` and any new contract
  invariants in `docs/runtime-contract.md`.

**Prerequisites:** A working credential path for the chosen agent
(API key in Key Vault for Claude Code; pre-seeded auth for `aider`).
Ideally Option A is done first so the second agent's session model
informs the choice between full restoration and
checkpoint-summary-injection.

### Option C: Observability Deepening

**One-liner:** Take Phase 1.3's traces-and-metrics scaffolding (PR #30
overlay: Prometheus, Grafana, Tempo) and instrument it end-to-end with
trace IDs in the NATS envelope, per-agent-type SLO dashboards, and
alert rules for the failure modes engineers actually see.

**Maturity gate:** Addresses
[**R6** ("⚡ Active - observability needed from day one") and **R15**
("🟡 Near-term - needed shortly after v1")](../research/hybrid-comms-architecture.md#open-questions-and-risk-register)
in the risk register. Both are flagged as either active or
near-term, so this option is pulling from the active-risk pool, not the
deferred pool.

**Estimated scope:** M. The infrastructure is already deployed; the
work is instrumentation, dashboard authoring, and alert rules.

**Strategic value:** Makes the test cluster diagnosable. Establishes the
SLO baselines that Option E (production hardening) will need to defend.
Without this, "why is this Job stuck" is answered by `kubectl logs` and
guesswork; with it, the on-call has a dashboard.

**Risks:**
- Scope creep. Observability work can expand without an exit criterion.
  Anchor to a single, testable acceptance: "if a worker fails in
  production, the on-call can diagnose root cause from the dashboard
  alone, without `kubectl logs`." If a finding is not visible from the
  dashboard, that is the bug to fix; if it is, the work is done.
- Premature optimization. Without traffic, the SLOs are theatre.
  Sequence this after Option B so there is at least one second agent's
  worth of traffic shape to instrument against.

**Concrete sub-tasks (sketch):**
- Trace ID propagation: NATS envelope header to proxy span to ACP
  session ID to agent stdout log. One `trace_id` end-to-end, queryable
  in Tempo.
- Per-agent-type SLOs: cold-start latency, queue depth, error rate,
  task-to-artifact latency. One Grafana dashboard per agent type plus a
  fleet overview.
- Alert rules: stuck Jobs (no progress for N minutes),
  ImagePullBackOff loops, NATS consumer lag above threshold, KEDA
  scaler errors. Routed to a real channel, not the void.
- Runbook entries for each alert: what it means, how to diagnose, how
  to mitigate.

**Prerequisites:** Option B (a second agent type) is strongly preferred
so the dashboards have more than one shape to render.

### Option D: Multi-Replica Orchestrator and Bridge `state.db` Postgres Migration

**One-liner:** Migrate the bridge's `state.db` from SQLite to Postgres
so the orchestrator can run with multiple replicas behind a service.

**Maturity gate:** Trigger for the deferred item "Bridge state.db
Postgres migration" is "Multi-replica orchestrator needed"
(see [plan.md Deferred table](plan.md#deferred-pending-validation) and
[research item 11](../research/hybrid-comms-architecture.md#deferred---validate-first-build-later)).
The trigger is **not** met. Single-instance orchestrator handles current
load.

**Estimated scope:** L. The `store.ts` module has a clean boundary
[per the research doc](../research/hybrid-comms-architecture.md#deferred---validate-first-build-later)
("the `store.ts` module has a clean boundary for future migration, but
the sync-to-async conversion is non-trivial"). The conversion alone is
weeks of refactor and re-testing.

**Strategic value:** None until multi-replica is a real near-term need.
Listed for completeness so the human can confirm the trigger has not
fired.

**Risks:**
- Spending L-scope effort on a deferred item that has no consumer is
  pure rework risk. By the time multi-replica is needed, Postgres
  schema requirements will likely be different.
- Sync-to-async conversion in `store.ts` touches every callsite in the
  bridge; a partial migration is worse than no migration.

**Concrete sub-tasks (sketch):** Not authored. Do not pick this option
unless the human is committing that multi-replica orchestrator is a
near-term requirement.

**Recommendation:** Do not pick this option now. Document the deferral
status in the next round of `docs/plan.md` updates and revisit when
load actually requires it.

**Prerequisites:** A concrete multi-replica orchestrator requirement.

### Option E: Production Hardening Track

**One-liner:** Promote the Phase 5 "test cluster" toward production
grade: Pod Security Admission enforcement, NetworkPolicy beyond
defaults, private cluster plus jumphost, Azure Policy compliance
baseline, real budget alerts, and vulnerability scanning beyond
Trivy-in-build.

**Maturity gate:** Not gated by a specific deferred item. It is the
natural follow-on to Phase 5's
[explicit non-goals](plan.md#non-goals)
("Production hardening (PSP/PSA tightening, network policies beyond
defaults)", "Private cluster, VPN, or hub-and-spoke networking",
"High availability"). Prerequisite for any "real customers" use case.

**Estimated scope:** L. Spans security, networking, and FinOps, each of
which is a coherent sub-track.

**Strategic value:** Required before daedalus can be exposed to anyone
outside the contributors. Without it, the cluster is fine for engineers
and CI but is not defensible against a hostile environment.

**Risks:**
- This is a product decision masquerading as an engineering one. The
  effort only makes sense if the human has decided daedalus needs a
  production tier (paying customers, internal SLA, regulatory exposure,
  whatever). Without that decision, hardening features the test
  cluster will never have users for.
- Scope is wide enough that it should be split into sub-phases (PSA
  and NetworkPolicy first; private networking second; FinOps third) or
  it will sprawl.

**Concrete sub-tasks (sketch):**
- PSA enforcement at `restricted` for the daedalus namespace; remediate
  any Pod that fails admission.
- NetworkPolicy: default-deny ingress and egress; explicit allow for
  NATS, agent egress to GitHub, and observability scrape paths.
- Private cluster with API server VNet integration and a jumphost; CI
  reaches the API via a self-hosted runner or a private DNS zone.
- Azure Policy baseline (CIS AKS) applied at the resource-group level;
  non-compliant resources fail Terraform plan.
- Budget alerts on the subscription and per-resource-group, wired to a
  notification channel.
- Continuous vulnerability scanning (Defender for Containers or
  equivalent), not just build-time Trivy.

**Prerequisites:** A product decision that daedalus needs a production
tier. Without it, this option is premature.

### Option F: K8s Operator with CRDs (`AgentRuntime`, `MCPServer`, `AgentCard`, `TaskPipeline`)

**One-liner:** Build the operator and CRDs designed in Phase 3 as the
end-state for dynamic agent reconfiguration.

**Maturity gate:** Trigger for the deferred item "K8s Operator with
CRDs" is "Factory reaches 5+ agent types" (see
[plan.md Deferred table](plan.md#deferred-pending-validation) and
[research item 9](../research/hybrid-comms-architecture.md#deferred---validate-first-build-later)).
The trigger is **not** met. The factory currently runs one agent type;
adding a second under Option B would still leave the count at 2. The
research doc is explicit: "At current scale, Helm charts with KEDA
ScaledJob templates provide the same functionality with a fraction of
the maintenance burden."

**Estimated scope:** L. Operator skeleton, four CRDs, reconciliation
loops, RBAC, controller-manager Helm chart, e2e tests, migration path
from the existing Helm-driven config.

**Strategic value:** None until the trigger fires. The operator solves
a maintenance-burden problem that does not exist yet.

**Risks:**
- Building it now bakes in assumptions about agent shape from a sample
  size of one (or two with Option B). Wait for 3-5 agents and the
  shared CRD shape is clearer.
- Operator code is a long-lived liability; building one before the
  problem exists means rewriting it once the problem actually exists.

**Concrete sub-tasks (sketch):** Not authored. Defer until the trigger
fires.

**Recommendation:** Do not pick this option now. Pick Option B two or
three times first; once manual Helm-value changes become the bottleneck
the research doc predicts, the operator scope will write itself.

**Prerequisites:** 5+ agent types in production, or concrete evidence
that manual Helm-value changes are the bottleneck.

### Option G: New Direction (Out-of-Band)

**One-liner:** A placeholder for a direction not currently captured in
[plan.md](plan.md) or the
[risk register](../research/hybrid-comms-architecture.md#open-questions-and-risk-register).

If the human wants to take daedalus somewhere not in the existing
roadmap (a customer-facing API, SaaS productization, a different agent
SDK, an on-prem distribution, anything else), document the proposal here
and we will add it to this file before picking. Out-of-band directions
need their own decision framework: what problem does this solve, what
existing investment does it preserve, what does it deprecate, and what
is the smallest experiment that would validate it.

**Estimated scope:** Unknown until the proposal is written.

**Maturity gate:** Author a proposal block here matching the sub-template
above. The proposal itself is the gate.

**Recommendation:** Only pick this if the human has a concrete direction
in mind. "Something new" without a proposal is a sign that the existing
options A through F have not been fully considered.

## Recommendation

- **Pick Option A first.** It is small, time-bounded, and unblocks
  roughly 40% of the Deferred (Pending Validation) table in one
  experiment. The information yield is high either way: a pass means
  R17 plus R18 are buildable as designed; a fail means the entire
  session-continuity track simplifies to checkpoint-summary-injection
  and a large pile of architecture (Postgres archive, blob storage,
  R18 decision tree) drops off the roadmap.
- **Then pick Option B.** The runtime contract has only ever been
  exercised against one agent. A second agent validates the abstraction
  is real, gives Option C a second traffic shape to instrument, and
  reduces the gap to Option F's trigger.
- **Defer Option C until A and B ship.** Observability work without
  traffic to instrument is theatre. Sequence it after Option B so there
  is real diversity to render.
- **Defer Options D, E, and F until their triggers fire.** D needs a
  multi-replica requirement. E needs a product decision that daedalus
  has a production tier. F needs 5+ agent types. None of those are
  true today.

## Out of Scope For This Doc

- Concrete sprint planning, time estimates, or assignment of work.
- Rewriting [plan.md](plan.md) to add Phase 6. That edit lands once an
  option is picked.
- Updates to the
  [Deferred table](plan.md#deferred-pending-validation) or the
  [risk register](../research/hybrid-comms-architecture.md#open-questions-and-risk-register).
- Any code changes.

## How To Pick

When ready, the human will:

1. Pick one option (Option A is the recommended starting point).
2. The agent will draft `docs/phase6-plan.md` and
   `docs/phase6-epic-draft.md` matching the
   [Phase 5 pattern](phase5-plan.md).
3. The agent will create the GitHub epic and start the
   Implement -> Review loop on sub-task 6.1.
