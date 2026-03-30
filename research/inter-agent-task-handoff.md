# Inter-Agent Task Handoff: Async Delegation & Callback Patterns for copilot-bridge

## Summary

copilot-bridge's current `ask_agent` tool provides synchronous, one-shot Q&A between agents - the caller blocks until the target responds (max 300s). This research examines patterns for **asynchronous task delegation** where Agent A can hand off a multi-step task to Agent B and receive results via callback, without the human acting as intermediary. We survey handoff patterns in LangGraph, AutoGen Swarm, CrewAI, OpenAI Agents SDK, and Google's A2A protocol, then propose a minimal bridge extension: a `delegate_task` tool backed by a SQLite task store, with results delivered to a channel or polling endpoint. The recommended approach builds on the existing `schedule` tool and `agent_calls` table rather than introducing an external message broker.

## Context & Motivation

The **Dark Software Factory** architecture (see [`research/dark-software-factory-architecture.md`](dark-software-factory-architecture.md)) envisions autonomous agent orchestration where specialized agents collaborate on complex workflows. Today, copilot-bridge supports inter-agent communication via `ask_agent`, but it has hard limits that prevent true task delegation:

1. **Synchronous blocking**: Agent A's session is frozen while waiting for Agent B. With a 300s maxTimeout, anything requiring iterative work (code generation → test → fix cycles) is infeasible.
2. **No structured task state**: There is no concept of a "task" that persists beyond a single call. No pending/in-progress/done lifecycle.
3. **No callback mechanism**: Agent B cannot proactively notify Agent A (or a channel) when work completes.
4. **No retry or error recovery**: If an ephemeral session times out, the result is lost. There's no retry queue.
5. **Depth ceiling**: The maxDepth=3 limit blocks deep delegation chains that would be natural in factory workflows (orchestrator → planner → implementer → reviewer).

These gaps matter because the Dark Factory pattern requires agents to dispatch long-running tasks (write a module, run tests, create a PR) to specialist agents and continue their own work. The human user should be able to kick off a workflow and see results arrive in a channel, not babysit each agent interaction.

### What this research does NOT cover
- External message broker integration (covered in [`research/message-broker-selection.md`](message-broker-selection.md))
- Kubernetes-level scaling (covered in [`research/keda-scaledjob-patterns.md`](keda-scaledjob-patterns.md))
- Agent definition authoring patterns

## Knowns

### copilot-bridge Internals (from source analysis)

- **`ask_agent` tool signature**: `{ target, message, agent?, timeout?, autopilot?, denyTools?, grantTools? }`. Handler validates allowlist, creates/extends `InterAgentContext`, calls `executeEphemeralCall()`, returns synchronously. Source: `src/core/session-manager.ts:1984-2069`.

- **InterAgentContext tracks call chains**: `{ chainId: UUID, visited: string[], depth: number, callerBot, callerChannel }`. The `chainId` is a UUID that persists across the entire call chain for auditing. Source: `src/core/inter-agent.ts:14-20`.

- **Allowlist is bidirectional**: Both `canCall` (on caller) and `canBeCalledBy` (on target) must permit the interaction. Wildcard `"*"` supported. Missing entries implicitly deny. Source: `src/core/inter-agent.ts:43-84`.

- **Ephemeral sessions are fully isolated**: Each `executeEphemeralCall()` creates a fresh `CopilotSession` with the target bot's workspace, `.env`, agent definition, and permission handler. Session is destroyed in a `finally` block. No state persists. Source: `src/core/session-manager.ts:1722-1837`.

- **Permission hierarchy in ephemeral sessions**: hardcoded safety denies → caller explicit denies → bridge custom tool auto-approves → caller explicit grants (only if caller has permission) → target's stored rules → caller channel's stored rules → autopilot fallback → deny. Source: `src/core/session-manager.ts:1891-1958`.

- **`schedule` tool already exists**: Supports cron (recurring) and ISO 8601 datetime (one-off) execution. Persists to SQLite `scheduled_tasks` table. Executes by sending a prompt to the channel's session. Survives bridge restarts. Source: `src/core/scheduler.ts`, `src/core/session-manager.ts:2467-2579`.

- **`agent_calls` table provides audit trail**: Schema: `{ id, caller_bot, target_bot, target_agent, message_summary(500), response_summary(500), duration_ms, success, error, chain_id, depth, created_at }`. Indexed on `created_at` and `chain_id`. Source: `src/state/store.ts:141-157`.

- **Bridge custom tools are auto-approved in ephemeral sessions**: `['send_file', 'show_file_in_chat', 'ask_agent', 'schedule', 'fetch_copilot_bridge_documentation', 'no_reply']`. Source: `src/core/session-manager.ts:36`.

- **Loop detector exists but is separate from inter-agent**: Tracks identical tool calls per channel (5 identical within 60s = flag, 10 = critical). Uses hash of tool name + args. Source: `src/core/loop-detector.ts`.

- **Agent discovery has priority ordering**: workspace `agents/` > `.github/agents/` > `~/.copilot/agents/` > plugins. Source: `src/core/inter-agent.ts:195-234`.

### External Framework Patterns

- **OpenAI Swarm / Agents SDK**: Handoff = returning an Agent from a function. The conversation transfers to the new agent, which sees the full message history. Stateless between `client.run()` calls. The Agents SDK (production successor to Swarm) adds `handoff()` with `on_handoff` callbacks, `input_filter` to control what history the receiving agent sees, and `input_type` for structured metadata (e.g., `{ reason, priority }`). Source: [OpenAI Agents SDK](https://openai.github.io/openai-agents-python/handoffs/), [OpenAI Swarm](https://github.com/openai/swarm).

- **AutoGen Swarm**: Agents generate `HandoffMessage` to signal transitions. All agents share the same message context. Speaker selection based on most recent `HandoffMessage`. Supports `HandoffTermination` to pause for user input. Source: [AutoGen Swarm docs](https://microsoft.github.io/autogen/stable/user-guide/agentchat-user-guide/swarm.html).

- **CrewAI Tasks**: First-class `Task` objects with `description`, `expected_output`, `agent`, `async_execution`, `callback`, `guardrail`, `context` (dependency on other tasks). Tasks can be sequential or hierarchical. `TaskOutput` includes `raw`, `pydantic`, `json_dict`. Guardrails validate output before passing to next task (max 3 retries). Source: [CrewAI Tasks docs](https://docs.crewai.com/concepts/tasks).

- **LangGraph**: Models workflows as state machines (graphs). Nodes are functions, edges determine transitions. State is a shared `TypedDict`/Pydantic model with reducer functions for merging updates. Supports parallel execution (super-steps), checkpointing, and human-in-the-loop breakpoints. Source: [LangGraph Graph API](https://docs.langchain.com/oss/python/langgraph/graph-api).

- **Google A2A Protocol**: Open protocol for inter-agent communication across frameworks. JSON-RPC 2.0 over HTTP(S). Agents publish "Agent Cards" describing capabilities. Supports sync request/response, streaming (SSE), and async push notifications. Tasks have lifecycle states. Designed for opaque agents that don't share internal state. Source: [A2A GitHub](https://github.com/a2aproject/A2A).

## Unknowns

- **Copilot CLI session memory limits**: How much context can an ephemeral session accumulate before degradation? This determines whether a delegated task agent can do iterative work (code → test → fix) within a single session, or needs to be checkpointed.

- **Concurrent ephemeral session limits**: Can the bridge run multiple `executeEphemeralCall()` instances in parallel? The `withWorkspaceEnv()` uses a mutex for `.env` injection - does this serialize all ephemeral calls for the same bot, or just protect the env setup phase?

- **Channel session resumption semantics**: When the `schedule` tool fires a prompt to a channel, does it create a new session or resume the existing one? If it resumes, the agent has context from prior interactions - this could be leveraged for callback delivery.

- **SQLite write contention under load**: If multiple agents are writing task state concurrently, does SQLite's single-writer model become a bottleneck? The bridge already uses SQLite for `agent_calls` and `scheduled_tasks`, so this is an operational concern.

- **GitHub Copilot CLI autopilot reliability**: In `autopilot` mode, does the ephemeral session reliably complete multi-step tasks without stalling? Or do some tool permission prompts still block?

- **Bridge restart recovery for in-flight tasks**: The `schedule` tool detects missed tasks on restart, but a delegated task that was in-progress when the bridge crashed has no recovery path today.

## Gaps

1. **No async execution primitive**: `ask_agent` blocks the caller. There is no fire-and-forget mechanism. The `schedule` tool is time-based, not event-based - you can schedule "run this prompt at time T" but not "run this when agent B finishes."

2. **No task lifecycle store**: No table or data structure tracks tasks through `pending → in_progress → completed/failed` states with results. The `agent_calls` table records completed calls but not in-flight work.

3. **No callback/notification mechanism**: When an async task completes, there is no way to notify the originating agent or channel. The bridge can post messages to channels, but there is no structured "task result delivery" pathway.

4. **No result persistence beyond 500 chars**: The `agent_calls` table truncates `response_summary` to 500 chars. For delegated tasks producing substantial output (code, reports), a separate result store is needed.

5. **No retry or dead-letter queue**: If an ephemeral session times out or errors, the failure is logged but the task is not retried. There is no configurable retry policy.

6. **No task cancellation**: Once an ephemeral call starts, there is no external API to cancel it. The only bound is the timeout.

7. **No structured output schema**: `ask_agent` returns free-text responses. For task delegation, structured results (status, artifacts, error details) would enable programmatic handling.

8. **No delegation-specific allowlist**: The existing allowlist governs `ask_agent`. Async delegation may need different permissions (e.g., "can delegate long-running tasks to" vs "can ask quick questions of").

## Analysis

### Handoff Pattern Taxonomy

Across the surveyed frameworks, four distinct patterns emerge for agent-to-agent task delegation:

#### Pattern 1: Synchronous Handoff (Conversation Transfer)
**Used by**: OpenAI Swarm, OpenAI Agents SDK, AutoGen Swarm

The calling agent transfers control of the conversation to another agent. The new agent sees the full (or filtered) message history and takes over. This is not delegation - it's a relay race. Only one agent is active at a time.

**Applicability to copilot-bridge**: This is essentially what `ask_agent` already does, but with the caller blocking. The Swarm pattern's `HandoffMessage` is a cleaner abstraction than copilot-bridge's tool-call-based approach, but the semantics are similar.

#### Pattern 2: Task Queue with Callback (Async Delegation)
**Used by**: CrewAI (with `async_execution=True`), A2A Protocol

A task is placed on a queue or submitted to a target agent. The caller continues its own work. When the task completes, a callback fires or the result is polled.

**Applicability to copilot-bridge**: This is the missing primitive. Requires: (a) a task store, (b) an async execution engine, (c) a callback or polling mechanism.

#### Pattern 3: Shared State Machine (Orchestrated Graph)
**Used by**: LangGraph

A central graph defines the workflow. Nodes (agents) read/write shared state. Edges determine transitions. The orchestrator manages execution order, parallelism, and checkpointing.

**Applicability to copilot-bridge**: Too heavyweight for the bridge's architecture. The bridge is a message relay, not a workflow engine. However, the concept of shared state (a task store) is directly applicable.

#### Pattern 4: Agent Cards + Discovery (Protocol-Level Interop)
**Used by**: Google A2A Protocol

Agents publish capability descriptions ("Agent Cards"). Clients discover agents, negotiate interaction modalities, and submit tasks via JSON-RPC. Supports sync, streaming, and async push notifications.

**Applicability to copilot-bridge**: The bridge already has agent discovery (`discoverAgentDefinitions`) and an allowlist. A2A's task lifecycle model (with states and push notifications) is a useful reference for designing the task store.

### Composing `schedule` + `ask_agent` for Pseudo-Async Handoff

**Can we approximate async delegation today without code changes?**

Partially. An agent could:
1. Call `schedule({ action: 'create', run_at: '<now + 5s>', prompt: 'Use ask_agent to ask bot-B to <task>. Post results to this channel.' })`
2. Continue its own work
3. The scheduled job fires, creates a session in the channel, calls `ask_agent`

**Limitations**:
- The scheduled job's session is a new session - it has no context from the original agent's conversation
- The 300s `maxTimeout` on `ask_agent` still applies - the delegated task must complete within that window
- Results arrive as free-text in the channel, not as structured data the original agent can programmatically consume
- No task tracking, retry, or cancellation
- The scheduled prompt is a string, not a structured task - fragile and hard to debug

**Verdict**: This is a workaround, not a solution. It demonstrates the pattern but is too fragile for production use.

### Minimal Extension: `delegate_task` Tool

The core proposal is a new tool, `delegate_task`, that extends the bridge with async task delegation:

```
delegate_task({
  target: string,           // Target bot name
  task: string,             // Task description (prompt for target agent)
  agent?: string,           // Agent persona for target
  callback_channel?: string, // Channel to post results (default: caller's channel)
  timeout?: number,         // Max execution time (default: 600s, max: 3600s)
  priority?: 'low' | 'normal' | 'high',
  retry?: { maxAttempts: number, backoffMs: number },
  autopilot?: boolean,
  denyTools?: string[],
  grantTools?: string[],
  metadata?: Record<string, string>  // Opaque key-value pairs
})
→ { taskId: string, status: 'queued' }
```

**Execution flow**:

```
Agent A                    Bridge                     Agent B
   │                         │                           │
   │─ delegate_task(B, ...) ─▶│                           │
   │◀─ { taskId, queued } ───│                           │
   │   (A continues work)    │                           │
   │                         │── executeEphemeralCall() ──▶│
   │                         │        (async, bg)         │
   │                         │                           │── does work...
   │                         │                           │── ...iterates...
   │                         │◀── response ──────────────│
   │                         │                           │
   │                         │── store result in tasks ──│
   │                         │── post to callback_channel │
   │                         │                           │
   ├─ (sees result in chat) ─│                           │
   │  OR                     │                           │
   │─ check_task(taskId) ───▶│                           │
   │◀─ { status, result } ──│                           │
```

### Task Store Schema

A new `delegated_tasks` SQLite table:

```sql
CREATE TABLE delegated_tasks (
  id TEXT PRIMARY KEY,                    -- UUID
  caller_bot TEXT NOT NULL,
  caller_channel TEXT NOT NULL,
  target_bot TEXT NOT NULL,
  target_agent TEXT,
  task_prompt TEXT NOT NULL,              -- Full prompt (no truncation)
  callback_channel TEXT,                  -- Where to deliver results
  status TEXT NOT NULL DEFAULT 'queued',  -- queued|running|completed|failed|cancelled|timed_out
  priority TEXT DEFAULT 'normal',
  result TEXT,                            -- Full result (no truncation)
  error TEXT,
  attempt INTEGER DEFAULT 0,
  max_attempts INTEGER DEFAULT 1,
  backoff_ms INTEGER DEFAULT 5000,
  timeout_ms INTEGER NOT NULL,
  metadata TEXT,                          -- JSON blob
  chain_id TEXT,                          -- Links to InterAgentContext.chainId
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  started_at TEXT,
  completed_at TEXT,
  next_retry_at TEXT
);

CREATE INDEX idx_tasks_status ON delegated_tasks(status);
CREATE INDEX idx_tasks_caller ON delegated_tasks(caller_bot, caller_channel);
CREATE INDEX idx_tasks_chain ON delegated_tasks(chain_id);
```

### Companion Tool: `check_task`

```
check_task({
  task_id?: string,         // Specific task
  status?: string,          // Filter by status
  limit?: number            // Max results (default 10)
})
→ { tasks: [{ id, status, target_bot, created_at, completed_at, result?, error? }] }
```

This allows the calling agent (or any agent with channel access) to poll for task completion.

### Callback Delivery Mechanism

Three options for delivering results:

#### Option A: Channel Message
Post a structured message to `callback_channel`:
```
🔔 **Task Completed** [task-id-short]
**From**: bot-B (agent: specialist)
**Status**: ✅ completed (47s)
**Result**: <truncated result>
Use `check_task({ task_id: "..." })` for full result.
```

**Pros**: Works with existing bridge infrastructure. Human-visible. Agent in channel can react.
**Cons**: Free-text in channel, requires parsing. Noisy if many tasks complete.

#### Option B: Direct Agent Callback
When task completes, send result to Agent A's session as a synthetic tool response.

**Pros**: Agent A gets structured data directly. Seamless programmatic flow.
**Cons**: Agent A's session may have ended. Requires session resumption logic. Complex.

#### Option C: Poll-Only (Task Store)
No proactive notification. Agent A periodically calls `check_task` to see if results are ready.

**Pros**: Simplest implementation. No new notification infrastructure.
**Cons**: Latency between completion and discovery. Agents must poll (wastes tokens/context).

### Safety & Security Considerations

#### Amplification Risk
Agent A delegates to B, which delegates to C, D, E... Each delegation spawns an ephemeral session consuming compute. Without limits:
- A single user message could trigger unbounded parallel work
- Each ephemeral session runs a full Copilot CLI instance

**Mitigations**:
- **Concurrency limit**: Max N delegated tasks running simultaneously per bot (e.g., 3)
- **Global task budget**: Max M tasks per chain_id (e.g., 10)
- **Rate limiting**: Max tasks per bot per hour
- **Reuse existing depth limit**: `delegate_task` should consume depth just like `ask_agent`

#### Runaway Loops
Agent A delegates task T to B. B's result triggers A to delegate T' to B. B's result triggers A to delegate T'' to B... Even without cycles in the call chain, the *task content* can create semantic loops.

**Mitigations**:
- **Task deduplication**: Hash task prompt + target + caller. Reject duplicates within a time window.
- **Chain budget**: Track total tasks spawned from a single `chain_id`. Hard cap (e.g., 20 tasks per chain).
- **Human circuit breaker**: After N tasks in a chain, require human confirmation to continue.

#### Resource Exhaustion
Long-running delegated tasks hold ephemeral sessions open. Each session consumes:
- A Copilot CLI process
- Memory for the session context
- File handles for workspace access

**Mitigations**:
- **Hard timeout ceiling**: 3600s absolute max for delegated tasks
- **Idle detection**: If the ephemeral session is idle for >60s, terminate and mark as timed_out
- **Memory monitoring**: Track session count, kill oldest if threshold exceeded

#### Permission Escalation
Agent A has limited permissions. It delegates to Agent B with `grantTools: ["bash"]`. If B's permission set is more permissive than A's, the delegation effectively escalates privileges.

**Mitigations**:
- **Inherit caller's ceiling**: Delegated tasks should never grant tools the caller doesn't have (already enforced in `buildEphemeralPermissionHandler`)
- **Separate delegation allowlist**: `canDelegateTo` distinct from `canCall`, allowing tighter control over who can spawn async work

## Options & Trade-offs

### Option 1: Channel-Based Callback (`delegate_task` + channel message)

| Aspect | Detail |
|--------|--------|
| **Description** | New `delegate_task` tool queues task in SQLite. Bridge executes async in background ephemeral session. Result posted to callback channel as a message. |
| **Pros** | Simple to implement. Human-visible audit trail. Works with existing channel infrastructure. Agents in channel can react to results. |
| **Cons** | Results are free-text in channel (noisy). No guaranteed delivery to calling agent specifically. |
| **Complexity** | **Medium** - New table, new tool, background execution loop, channel posting |
| **Recommendation** | **Yes - Start here** |

### Option 2: Task Store + Polling (`delegate_task` + `check_task`)

| Aspect | Detail |
|--------|--------|
| **Description** | Same as Option 1 but without channel notification. Agent A polls `check_task` to discover completion. |
| **Pros** | Simplest possible implementation. Fully programmatic. No channel noise. |
| **Cons** | Polling wastes tokens/context. Latency between completion and discovery. Agent A must be actively running to poll. |
| **Complexity** | **Low** - New table, two new tools, background execution loop |
| **Recommendation** | **Yes - as companion to Option 1** |

### Option 3: Hybrid (Channel + Poll + Schedule Trigger)

| Aspect | Detail |
|--------|--------|
| **Description** | Combine Options 1 & 2. Results stored in task table AND posted to channel. Calling agent can poll or react to channel message. Additionally, register a one-off `schedule` job that fires when the task completes, sending a prompt to the calling agent's channel like "Task X completed. Use check_task to review." |
| **Pros** | Multiple notification paths. Agent can be notified even if it wasn't polling. Human can see progress. |
| **Cons** | More moving parts. Schedule-based notification adds latency if poll-to-schedule delay. |
| **Complexity** | **Medium-High** - Combines all mechanisms |
| **Recommendation** | **Maybe - Good target state, but start with Option 1+2** |

### Option 4: Full A2A Protocol Implementation

| Aspect | Detail |
|--------|--------|
| **Description** | Implement Google's A2A protocol: Agent Cards, JSON-RPC task submission, SSE streaming, push notifications. |
| **Pros** | Standards-based. Interoperable with other A2A-compliant agents. Rich task lifecycle. |
| **Cons** | Massive scope increase. Requires HTTP server per agent. Over-engineered for bridge's current architecture. |
| **Complexity** | **Very High** - New protocol layer, HTTP endpoints, discovery service |
| **Recommendation** | **No - Not appropriate for current scale. Revisit if bridge becomes multi-tenant.** |

### Option 5: External Message Broker (Redis/NATS)

| Aspect | Detail |
|--------|--------|
| **Description** | Delegate tasks via external message queue. Worker agents consume from queue, publish results to result queue. |
| **Pros** | Battle-tested async pattern. Natural scaling. Decoupled producers/consumers. |
| **Cons** | New infrastructure dependency. Overkill for single-bridge deployment. See [`research/message-broker-selection.md`](message-broker-selection.md). |
| **Complexity** | **High** - New dependency, queue management, consumer lifecycle |
| **Recommendation** | **No for now - Appropriate when scaling to multiple bridge instances** |

## Recommendations

### 1. Implement `delegate_task` and `check_task` tools (Priority: High)

Add two new tools to the bridge:

- **`delegate_task`**: Validates allowlist (reuse `canCall` or add `canDelegateTo`), creates a `delegated_tasks` row with status `queued`, spawns a background `executeEphemeralCall()` (not awaited by the caller), and returns `{ taskId, status: 'queued' }` immediately.

- **`check_task`**: Queries the `delegated_tasks` table. Returns task status, result (if completed), and error (if failed). Supports filtering by `task_id`, `status`, `caller_bot`.

Implementation touches:
- New file: `src/core/task-delegator.ts` (task store CRUD, background execution orchestrator)
- New table: `delegated_tasks` in `src/state/store.ts`
- New tool registrations in `src/core/session-manager.ts`
- Extend `BRIDGE_CUSTOM_TOOLS` to include `delegate_task` and `check_task`

Estimated effort: ~400 lines of new code, 1-2 days.

### 2. Deliver results via channel message + task store (Priority: High)

When a delegated task completes, do both:
- Store full result in `delegated_tasks.result` (no truncation)
- Post a summary message to `callback_channel` (or caller's channel if not specified)

This gives humans visibility and agents a structured query path.

### 3. Add concurrency and budget limits (Priority: High - ship with #1)

Do NOT ship `delegate_task` without these safety rails:
- `maxConcurrentDelegations` per bot (default: 3)
- `maxTasksPerChain` per chain_id (default: 10)
- `maxDelegationTimeout` (default: 600s, absolute max: 3600s)
- Task prompt deduplication within a 5-minute window

### 4. Increase `maxTimeout` for delegated tasks (Priority: Medium)

The current 300s `maxTimeout` is adequate for `ask_agent` (quick questions) but too short for delegated tasks (write code, run tests). Allow `delegate_task` to use a separate `maxDelegationTimeout` (default: 600s, configurable up to 3600s).

### 5. Add retry with exponential backoff (Priority: Medium)

If a delegated task fails (timeout, error), automatically retry up to `maxAttempts` times with configurable backoff. Record each attempt in the task row. After exhausting retries, mark as `failed` and notify.

### 6. Consider a separate `canDelegateTo` allowlist (Priority: Low)

Currently, `canCall` governs both sync questions and async delegation. As the system matures, it may be valuable to have separate allowlists:
- `canCall`: For quick sync Q&A (existing `ask_agent`)
- `canDelegateTo`: For long-running async tasks (new `delegate_task`)

This allows an admin to permit bot A to ask bot B quick questions but NOT delegate compute-intensive tasks.

### 7. Do NOT implement full A2A or external broker yet (Priority: Low)

The bridge is a single-process Node.js service. SQLite-backed task delegation is the right complexity level. When the architecture evolves to multiple bridge instances or cross-network agent communication, revisit A2A protocol alignment and external message brokers.

## Follow-up Research

- [ ] **Ephemeral session concurrency testing**: Run 5+ parallel `executeEphemeralCall()` instances against the same target bot. Measure: memory usage, Copilot CLI stability, SQLite write contention, mutex behavior of `withWorkspaceEnv()`. This determines the safe `maxConcurrentDelegations` default.

- [ ] **Session resumption for callback delivery**: Investigate whether a completed task's result can be injected into Agent A's *existing* session as a synthetic message, rather than posted to the channel. This would enable seamless programmatic consumption. Requires understanding Copilot CLI session lifecycle and message injection APIs.

- [ ] **Checkpoint/resume for long-running tasks**: For tasks exceeding 10 minutes, can the ephemeral session be checkpointed (save context to disk) and resumed? This would allow the bridge to free resources and resume work later. Related to LangGraph's checkpointing concept.

- [ ] **Task DAG support**: `delegate_task` as proposed is a single task. For complex workflows (A depends on B and C, D depends on A), a task DAG abstraction would be needed. Research whether this should live in the bridge or in agent-authored workflow definitions.

- [ ] **Observability and tracing**: The `chain_id` in `delegated_tasks` enables tracing across async boundaries. Research integration with OpenTelemetry or similar tracing systems to visualize multi-agent task flows.

- [ ] **Human-in-the-loop approval for delegation**: Should there be a config option where `delegate_task` requires human approval before execution starts? This would be analogous to CrewAI's `human_input=True` on tasks.

## Open Questions

- **Should delegated tasks inherit the caller's full context (conversation history)?** The current `executeEphemeralCall` sends only the task prompt. For complex delegations, the target agent might benefit from knowing the conversation context. But this increases token cost and may leak sensitive information. This needs a policy decision, not more research.

- **What happens when the bridge restarts mid-delegation?** The `scheduled_tasks` table has restart recovery (missed tasks detected on startup). The `delegated_tasks` table would need similar logic: on startup, mark any `running` tasks as `failed` (or `timed_out`) and optionally re-queue them based on retry policy.

- **Should delegated tasks run in the target bot's channel or a dedicated "work" channel?** Running in the bot's channel means the human sees the agent working. Running in a dedicated work channel keeps the main channel clean but reduces visibility. This is a UX decision.

- **How should the bridge handle a delegated task that itself calls `delegate_task`?** This creates nested async delegation. The `maxTasksPerChain` budget handles the safety aspect, but the depth-tracking semantics become complex: does async delegation increment depth? If not, the maxDepth limit is easily bypassed.

- **Is there value in a `cancel_task` tool?** If Agent A realizes the delegated task is no longer needed, can it cancel the in-progress ephemeral session? This requires the bridge to track session handles for delegated tasks and support graceful termination.

## References

- [copilot-bridge source](https://github.com/ChrisRomp/copilot-bridge) - Primary source for `ask_agent`, `executeEphemeralCall`, `schedule`, `agent_calls` table, allowlist, and inter-agent context management.
- [OpenAI Agents SDK - Handoffs](https://openai.github.io/openai-agents-python/handoffs/) - Production handoff API: `handoff()` with `on_handoff` callbacks, `input_filter`, `input_type` for structured metadata.
- [OpenAI Swarm (experimental)](https://github.com/openai/swarm) - Educational framework demonstrating lightweight agent handoff via function returns. Now superseded by Agents SDK.
- [AutoGen Swarm](https://microsoft.github.io/autogen/stable/user-guide/agentchat-user-guide/swarm.html) - `HandoffMessage`-based agent transitions with shared message context. Supports `HandoffTermination` for human-in-the-loop.
- [CrewAI Tasks](https://docs.crewai.com/concepts/tasks) - First-class `Task` objects with `async_execution`, `callback`, `guardrail`, `context` (inter-task dependencies), and structured `TaskOutput`.
- [LangGraph Graph API](https://docs.langchain.com/oss/python/langgraph/graph-api) - State machine approach: nodes, edges, shared state with reducers, checkpointing, super-steps for parallel execution.
- [Google A2A Protocol](https://github.com/a2aproject/A2A) - Open protocol for inter-agent communication. JSON-RPC 2.0, Agent Cards for discovery, sync/streaming/async push notification modes.
- [`research/dark-software-factory-architecture.md`](dark-software-factory-architecture.md) - Dark Factory architecture vision that motivates the need for async agent delegation.
- [`research/message-broker-selection.md`](message-broker-selection.md) - Message broker research relevant to future scaling of task delegation beyond single-bridge deployment.
- [`research/keda-scaledjob-patterns.md`](keda-scaledjob-patterns.md) - KEDA scaling patterns relevant to auto-scaling agent worker pods based on task queue depth.
