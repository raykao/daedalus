# SIGTERM and Graceful Shutdown Behavior

This document describes how the daedalus proxy handles `SIGTERM` (and `SIGINT`) signals, the grace period phases, Kubernetes recommendations, and the behavior in each operational scenario.

## Signal Chain

```
Kubernetes / operator
        |
        | SIGTERM
        v
  daedalus proxy
        |
        | ACP session/cancel (JSON-RPC 2.0 over TCP)
        v
  ACP agent (Copilot CLI or compatible)
        |
        | graceful stop
        v
     (exit)
```

1. The OS delivers `SIGTERM` to the proxy process.
2. The proxy stops accepting new NATS messages immediately (consumer context is cancelled).
3. The proxy waits up to the grace period for any in-flight ACP session to complete.
4. If the grace period expires, the proxy sends `session/cancel` to the ACP agent for every active session.
5. The proxy waits up to 5 s for Handle calls to drain after cancellation.
6. The proxy exits cleanly.

## Grace Period Phases

| Phase | Trigger | Duration | Action |
|-------|---------|----------|--------|
| 1 - Stop ingestion | SIGTERM received | immediate | Consumer stops fetching new NATS messages |
| 2 - Wait for in-flight | Phase 1 complete | up to `--grace-period` (default 30 s) | Block until all Handle calls finish |
| 3 - Cancel sessions | Grace period expires | immediate | Send ACP `session/cancel` for each active session |
| 4 - Exit buffer | Phase 3 complete | up to 5 s | Wait for Handle calls to drain after cancellation |
| 5 - Force exit | Exit buffer expires | immediate | Log warning and return error; process exits |

### Timeline (default 30 s grace period)

```
t=0      SIGTERM received
t=0      Phase 1: consumer stops fetching
t=0..30  Phase 2: waiting for in-flight message (if any)
  - clean case: message finishes at t=N (N < 30) -> proxy exits at t=N
  - stuck case: message still running at t=30
t=30     Phase 3: ACP session/cancel sent
t=30..35 Phase 4: waiting for Handle to drain
  - usually immediate once agent sees cancel
t=35     Phase 5: force exit (if still not done)
```

## Kubernetes Configuration

### Recommended `terminationGracePeriodSeconds`

Set `terminationGracePeriodSeconds: 35` in the Pod spec:

```yaml
spec:
  terminationGracePeriodSeconds: 35   # 30 s app grace + 5 s K8s buffer
  containers:
  - name: daedalus-proxy
    args:
    - --grace-period=30s
```

The 5-second buffer gives Kubernetes time to send the final `SIGKILL` only after the app has already exited. If the app takes the full 35 s, Kubernetes sends `SIGKILL` at t=35.

### Recommended `preStop` Hook

If the proxy is behind a Kubernetes Service, add a `preStop` sleep to let existing connections drain before `SIGTERM` is delivered:

```yaml
lifecycle:
  preStop:
    exec:
      command: ["/bin/sleep", "5"]
```

With this hook the effective timeline becomes:

```
t=0   preStop hook runs (sleep 5 s)
t=5   SIGTERM delivered to proxy
t=5   Phase 1: stop ingestion
t=35  K8s terminationGracePeriodSeconds expires, SIGKILL sent
```

Set `terminationGracePeriodSeconds: 40` when using the `preStop` hook (5 s hook + 30 s grace + 5 s buffer).

## Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--grace-period` | `GRACE_PERIOD` | `30s` | App-level grace period before ACP sessions are cancelled |

## Behavior by Scenario

| Scenario | In-flight message? | Outcome |
|----------|-------------------|---------|
| **Idle** - no messages being processed | No | Proxy exits immediately on SIGTERM |
| **Processing** - message finishes within grace period | Yes, completes | Proxy waits, then exits cleanly (no error) |
| **Processing** - message exceeds grace period | Yes, still running | ACP `session/cancel` sent; proxy exits after session drains |
| **Stuck agent** - agent ignores cancel | Yes, never completes | Proxy force-exits after exit buffer (5 s post-cancel); logs warning |
| **Multiple messages** | Multiple (if consumer runs concurrently) | All sessions cancelled; proxy waits for all Handles to drain |

## Implementation Notes

- The NATS consumer context and the ACP "work context" are separate. When SIGTERM cancels the consumer context, it does NOT cancel the ACP work context. This ensures in-flight `session/prompt` calls are not aborted prematurely.
- The `ShutdownManager.WorkContext()` context is only cancelled after the grace period (or when all messages complete). Wire it in `main.go` as the context passed to `handler.Handle`.
- Each `session/cancel` call has a 5-second timeout to prevent a hung ACP agent from blocking shutdown indefinitely.
- Structured JSON logs (`slog`) are emitted at each shutdown phase with the key `phase` for easy filtering.
