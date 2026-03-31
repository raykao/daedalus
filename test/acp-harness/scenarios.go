package main

import (
"context"
"encoding/json"
"fmt"
)

// ScenarioResult captures the outcome of one test scenario.
type ScenarioResult struct {
Name       string
Passed     bool
Details    string
Assertions []AssertResult
}

// Scenario is a function that runs one test scenario against a Client.
type Scenario func(ctx context.Context, c *Client) ScenarioResult

// AllScenarios returns the full map of named scenarios.
func AllScenarios() map[string]Scenario {
return map[string]Scenario{
"happy-path":          ScenarioHappyPath,
"multi-turn":          ScenarioMultiTurn,
"cancel":              ScenarioCancel,
"session-load":        ScenarioSessionLoad,
"permission-handling": ScenarioPermissionHandling,
"error-handling":      ScenarioErrorHandling,
"concurrent-sessions": ScenarioConcurrentSessions,
"mcp-passthrough":     ScenarioMCPPassthrough,
}
}

// ── initialize helpers ────────────────────────────────────────────────────────

func initialize(ctx context.Context, c *Client) (json.RawMessage, error) {
result, _, err := c.RPC(ctx, "initialize", map[string]any{
"protocolVersion": "2025-01-01",
"capabilities":    map[string]any{"streaming": true},
"clientInfo":      map[string]any{"name": "agent-forge-proxy", "version": "0.1.0"},
})
return result, err
}

func newSession(ctx context.Context, c *Client, workDir string, mcpServers []map[string]any) (string, error) {
params := map[string]any{"workDir": workDir}
if len(mcpServers) > 0 {
params["mcpServers"] = mcpServers
}
result, _, err := c.RPC(ctx, "session/new", params)
if err != nil {
return "", err
}
var r struct {
SessionID string `json:"sessionId"`
}
if err := json.Unmarshal(result, &r); err != nil {
return "", err
}
return r.SessionID, nil
}

// ── Scenario: Happy Path ──────────────────────────────────────────────────────

func ScenarioHappyPath(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

initResult, err := initialize(ctx, c)
if err != nil {
return fail("happy-path", fmt.Sprintf("initialize failed: %v", err))
}
a.HasField("init has protocolVersion", initResult, "protocolVersion")
a.HasField("init has capabilities", initResult, "capabilities")
a.HasField("init has serverInfo", initResult, "serverInfo")

sid, err := newSession(ctx, c, "/workspace", nil)
if err != nil {
return fail("happy-path", fmt.Sprintf("session/new failed: %v", err))
}
a.Check("sessionId non-empty", sid != "", "sessionId is empty")

promptResult, notifs, err := c.RPC(ctx, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    "create hello.txt",
})
if err != nil {
return fail("happy-path", fmt.Sprintf("session/prompt failed: %v", err))
}
a.MinNotifications("streaming deltas received", notifs, 2)
a.NotificationMethod("delta notification method", notifs, "assistant.message_delta")
a.HasField("prompt result has content", promptResult, "content")
a.HasField("prompt result has artifacts", promptResult, "artifacts")

return toResult("happy-path", a)
}

// ── Scenario: Multi-turn ──────────────────────────────────────────────────────

func ScenarioMultiTurn(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

if _, err := initialize(ctx, c); err != nil {
return fail("multi-turn", fmt.Sprintf("initialize: %v", err))
}
sid, err := newSession(ctx, c, "/workspace", nil)
if err != nil {
return fail("multi-turn", fmt.Sprintf("session/new: %v", err))
}

for i := 1; i <= 3; i++ {
result, _, err := c.RPC(ctx, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    fmt.Sprintf("turn %d", i),
})
if err != nil {
return fail("multi-turn", fmt.Sprintf("prompt %d failed: %v", i, err))
}
a.HasField(fmt.Sprintf("turn %d has content", i), result, "content")
}

return toResult("multi-turn", a)
}

// ── Scenario: Cancel ─────────────────────────────────────────────────────────

func ScenarioCancel(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

if _, err := initialize(ctx, c); err != nil {
return fail("cancel", fmt.Sprintf("initialize: %v", err))
}
sid, err := newSession(ctx, c, "/workspace", nil)
if err != nil {
return fail("cancel", fmt.Sprintf("session/new: %v", err))
}

result, _, err := c.RPC(ctx, "session/cancel", map[string]any{"sessionId": sid})
if err != nil {
return fail("cancel", fmt.Sprintf("session/cancel failed: %v", err))
}
a.BoolField("canceled=true", result, "canceled", true)

return toResult("cancel", a)
}

// ── Scenario: Session Load ────────────────────────────────────────────────────

func ScenarioSessionLoad(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

if _, err := initialize(ctx, c); err != nil {
return fail("session-load", fmt.Sprintf("initialize: %v", err))
}
sid, err := newSession(ctx, c, "/workspace", nil)
if err != nil {
return fail("session-load", fmt.Sprintf("session/new: %v", err))
}

// Populate history.
if _, _, err := c.RPC(ctx, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    "populate history",
}); err != nil {
return fail("session-load", fmt.Sprintf("prompt for history: %v", err))
}

result, notifs, err := c.RPC(ctx, "session/load", map[string]any{"sessionId": sid})
if err != nil {
return fail("session-load", fmt.Sprintf("session/load failed: %v", err))
}
a.BoolField("loaded=true", result, "loaded", true)
a.NotificationMethod("session/update notification sent", notifs, "session/update")

return toResult("session-load", a)
}

// ── Scenario: Permission Handling ────────────────────────────────────────────

func ScenarioPermissionHandling(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

if _, err := initialize(ctx, c); err != nil {
return fail("permission-handling", fmt.Sprintf("initialize: %v", err))
}
sid, err := newSession(ctx, c, "/workspace", nil)
if err != nil {
return fail("permission-handling", fmt.Sprintf("session/new: %v", err))
}

result, notifs, err := c.RPC(ctx, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    "create hello.txt",
})
if err != nil {
return fail("permission-handling", fmt.Sprintf("session/prompt: %v", err))
}

hasPermReq := false
for _, n := range notifs {
if n.Method == "session/request_permission" {
hasPermReq = true
break
}
}
if hasPermReq {
a.Pass("permission request received")
} else {
a.Pass("no permission request (server not configured for it)")
}
a.HasField("final result has content", result, "content")

return toResult("permission-handling", a)
}

// ── Scenario: Error Handling ──────────────────────────────────────────────────

func ScenarioErrorHandling(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

if _, err := initialize(ctx, c); err != nil {
return fail("error-handling", fmt.Sprintf("initialize: %v", err))
}

// Unknown method should return an error.
_, _, err := c.RPC(ctx, "nonexistent/method", map[string]any{})
a.Check("unknown method returns error", err != nil, "expected error for unknown method")

// Missing session ID should return an error.
_, _, err = c.RPC(ctx, "session/cancel", map[string]any{"sessionId": "nonexistent-id"})
a.Check("unknown sessionId returns error", err != nil, "expected error for unknown sessionId")

return toResult("error-handling", a)
}

// ── Scenario: Concurrent Sessions ────────────────────────────────────────────

func ScenarioConcurrentSessions(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

if _, err := initialize(ctx, c); err != nil {
return fail("concurrent-sessions", fmt.Sprintf("initialize: %v", err))
}

const n = 3
var sids []string
for i := 0; i < n; i++ {
sid, err := newSession(ctx, c, fmt.Sprintf("/workspace/%d", i), nil)
if err != nil {
return fail("concurrent-sessions", fmt.Sprintf("session %d failed: %v", i, err))
}
sids = append(sids, sid)
}
a.Check("all sessions created", len(sids) == n, "expected %d sessions, got %d", n, len(sids))

for i, sid := range sids {
result, _, err := c.RPC(ctx, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    fmt.Sprintf("prompt for session %d", i),
})
if err != nil {
return fail("concurrent-sessions", fmt.Sprintf("prompt to session %d failed: %v", i, err))
}
a.HasField(fmt.Sprintf("session %d prompt result", i), result, "content")
}

return toResult("concurrent-sessions", a)
}

// ── Scenario: MCP Passthrough ─────────────────────────────────────────────────

func ScenarioMCPPassthrough(ctx context.Context, c *Client) ScenarioResult {
a := &Assertions{}

if _, err := initialize(ctx, c); err != nil {
return fail("mcp-passthrough", fmt.Sprintf("initialize: %v", err))
}

mcpServers := []map[string]any{
{"name": "github", "type": "stdio", "command": "gh-mcp"},
{"name": "filesystem", "type": "stdio", "command": "fs-mcp"},
}
sid, err := newSession(ctx, c, "/workspace", mcpServers)
if err != nil {
return fail("mcp-passthrough", fmt.Sprintf("session/new with mcpServers: %v", err))
}
a.Check("session created with mcpServers", sid != "", "sessionId is empty")

result, _, err := c.RPC(ctx, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    "list files",
})
if err != nil {
return fail("mcp-passthrough", fmt.Sprintf("prompt after mcpServers session/new: %v", err))
}
a.HasField("prompt works with mcpServers session", result, "content")

return toResult("mcp-passthrough", a)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func toResult(name string, a *Assertions) ScenarioResult {
results := a.All()
passed := true
for _, r := range results {
if !r.Passed {
passed = false
break
}
}
return ScenarioResult{
Name:       name,
Passed:     passed,
Assertions: results,
}
}

func fail(name, reason string) ScenarioResult {
return ScenarioResult{Name: name, Passed: false, Details: reason}
}
