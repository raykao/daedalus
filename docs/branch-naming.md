# Branch Naming Convention

This document describes the structured branch naming convention used by daedalus worker sessions.

## Convention

All worker agent branches follow the format:

```
agent/<feature>/<agent-name>/<session-id>
```

| Component | Description | Example |
|-----------|-------------|---------|
| `agent/` | Fixed prefix - namespaces all agent branches | `agent/` |
| `<feature>` | Sanitized task or feature identifier | `fix-login-bug` |
| `<agent-name>` | The agent type executing the task | `copilot`, `claude`, `codex` |
| `<session-id>` | 32-char hex string from `crypto/rand` (16 bytes) | `a3f2b1c4d5e6f7a8b9c0d1e2f3a4b5c6` |

### Examples

```
agent/fix-login-bug/copilot/abc123def456abc123def456abc123de
agent/add-api-tests/claude/sess-789aabbccddeeff0011223344556677
agent/refactor-db/codex/a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4
```

## Why This Format

**Namespace isolation**: The `agent/` prefix makes it trivial to list or clean up all agent branches:

```bash
git branch --list 'agent/*'
git branch -r --list 'origin/agent/*'
```

**Retry visibility**: Because `<feature>/<agent-name>` uniquely identifies a work unit, listing all sessions is straightforward - sort by creation time to see history.

**Easy cleanup**: After merging, all branches for a feature can be pruned:

```bash
git branch -r --list "origin/agent/fix-login-bug/*" | xargs -I{} git push origin --delete {}
```

**No UUID dependency**: Session IDs use `crypto/rand` with hex encoding, avoiding an external UUID library while still providing sufficient uniqueness.

## Sanitization

Branch component strings are sanitized to produce valid git ref names. The following characters are replaced with hyphens (`-`):

- Whitespace
- `.` (dot - avoids `..` sequences)
- `@` (avoids `@{` sequences)
- `~`, `^`, `:`, `?`, `*`, `[`, `\`
- `/` (used as the component delimiter)
- ASCII control characters (0x00-0x1f) and DEL (0x7f)

Consecutive hyphens are collapsed to a single hyphen, and leading/trailing hyphens are removed.

## Retry Detection

When a worker agent starts, the `Detector` queries remote branches to check whether previous sessions exist for the same `<feature>/<agent-name>` pair:

```
FindRetries(ctx, feature, agentName) -> []BranchName
IsRetry(ctx, feature, agentName)     -> (bool, attemptCount, error)
LatestSession(ctx, feature, agentName) -> *BranchName
```

The `GitBranchLister` implementation runs:

```bash
git branch -r --list 'origin/agent/*'
```

and parses each ref through `Parse()`.

### Retry detection algorithm

1. List all remote branches matching `origin/agent/*`
2. Parse each into a `BranchName`
3. Filter by `Feature == sanitize(feature) && AgentName == sanitize(agentName)`
4. Return the filtered list

If the list is non-empty, this is a retry. The attempt number is `len(matches) + 1`.

## WorkerSession Lifecycle

```go
w := &branch.WorkerSession{
    Feature:   "fix-login-bug",
    AgentName: "copilot",
    RepoDir:   "/path/to/repo",
}

// Start: generates session ID, creates branch, checks out
b, err := w.Start(ctx)

// ... do work on the branch ...

// Complete: push to remote
err = w.Complete(ctx)

// OR on failure:
err = w.Abort(ctx, "task timed out")
```

## Integration with Proxy (Future)

The proxy will create a `WorkerSession` before dispatching a task to a worker agent:

1. Proxy receives task from NATS queue
2. Proxy calls `WorkerSession.Start(ctx)` to create the branch
3. Branch name is passed to the worker agent as an environment variable or task parameter
4. Worker agent commits all code changes to this branch
5. On success: proxy calls `WorkerSession.Complete(ctx)` - branch is pushed and ready for PR
6. On failure: proxy calls `WorkerSession.Abort(ctx, reason)` - branch is cleaned up, task is requeued

This ensures every task execution has a traceable branch, and retry history is visible on GitHub.
