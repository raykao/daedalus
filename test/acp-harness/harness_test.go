package main

import (
"bufio"
"context"
"encoding/json"
"fmt"
"net"
"sync"
"sync/atomic"
"testing"
"time"
)

// ── embedded mini mock server ─────────────────────────────────────────────────

type miniServer struct {
listener net.Listener
mu       sync.Mutex
sessions map[string]bool
}

var miniSessionCounter atomic.Int64

func startMiniServer(t *testing.T) (string, *miniServer) {
t.Helper()
ln, err := net.Listen("tcp", "127.0.0.1:0")
if err != nil {
t.Fatalf("listen: %v", err)
}
ms := &miniServer{listener: ln, sessions: make(map[string]bool)}
go ms.serve()
t.Cleanup(func() { ln.Close() })
return ln.Addr().String(), ms
}

func (ms *miniServer) serve() {
for {
c, err := ms.listener.Accept()
if err != nil {
return
}
go ms.handleConn(c)
}
}

func (ms *miniServer) addSession(id string) {
ms.mu.Lock()
ms.sessions[id] = true
ms.mu.Unlock()
}

func (ms *miniServer) hasSession(id string) bool {
ms.mu.Lock()
defer ms.mu.Unlock()
return ms.sessions[id]
}

func (ms *miniServer) handleConn(c net.Conn) {
defer c.Close()
enc := json.NewEncoder(c)
scanner := bufio.NewScanner(c)

send := func(v any) { enc.Encode(v) } //nolint:errcheck
respond := func(id *int64, result any) {
send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
rpcError := func(id *int64, code int, msg string) {
send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}
notify := func(method string, params any) {
send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

for scanner.Scan() {
var req struct {
JSONRPC string          `json:"jsonrpc"`
ID      *int64          `json:"id"`
Method  string          `json:"method"`
Params  json.RawMessage `json:"params"`
}
if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
continue
}

switch req.Method {
case "initialize":
respond(req.ID, map[string]any{
"protocolVersion": "2025-01-01",
"capabilities":    map[string]any{"streaming": true, "loadSession": true, "permissionRequest": true},
"serverInfo":      map[string]any{"name": "mini-mock", "version": "0.0.1"},
})

case "session/new":
n := miniSessionCounter.Add(1)
sid := fmt.Sprintf("sess-%d", n)
ms.addSession(sid)
respond(req.ID, map[string]any{"sessionId": sid})

case "session/prompt":
var p struct {
SessionID string `json:"sessionId"`
}
json.Unmarshal(req.Params, &p)
if !ms.hasSession(p.SessionID) {
rpcError(req.ID, -32602, "unknown sessionId")
continue
}
notify("assistant.message_delta", map[string]any{"sessionId": p.SessionID, "content": "chunk1"})
notify("assistant.message_delta", map[string]any{"sessionId": p.SessionID, "content": "chunk2"})
respond(req.ID, map[string]any{
"sessionId": p.SessionID,
"content":   "done",
"artifacts": []map[string]any{{"path": "out.txt", "content": "hello"}},
})

case "session/cancel":
var p struct {
SessionID string `json:"sessionId"`
}
json.Unmarshal(req.Params, &p)
if !ms.hasSession(p.SessionID) {
rpcError(req.ID, -32602, "unknown sessionId")
continue
}
respond(req.ID, map[string]any{"sessionId": p.SessionID, "canceled": true})

case "session/load":
var p struct {
SessionID string `json:"sessionId"`
}
json.Unmarshal(req.Params, &p)
if !ms.hasSession(p.SessionID) {
rpcError(req.ID, -32602, "unknown sessionId")
continue
}
notify("session/update", map[string]any{"sessionId": p.SessionID, "history": []any{}})
respond(req.ID, map[string]any{"sessionId": p.SessionID, "loaded": true})

default:
rpcError(req.ID, -32601, "method not found")
}
}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func makeTestClient(t *testing.T, addr string) *Client {
t.Helper()
deadline := time.Now().Add(2 * time.Second)
var lastErr error
for time.Now().Before(deadline) {
c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
if err == nil {
return &Client{
conn:    c,
scanner: bufio.NewScanner(c),
enc:     json.NewEncoder(c),
}
}
lastErr = err
time.Sleep(20 * time.Millisecond)
}
t.Fatalf("could not connect to mini server: %v", lastErr)
return nil
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestClientRPC_Initialize(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

result, notifs, err := c.RPC(context.Background(), "initialize", map[string]any{
"protocolVersion": "2025-01-01",
"capabilities":    map[string]any{"streaming": true},
"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
})
if err != nil {
t.Fatalf("initialize: %v", err)
}
if len(notifs) != 0 {
t.Errorf("expected 0 notifications, got %d", len(notifs))
}

a := &Assertions{}
a.HasField("protocolVersion", result, "protocolVersion")
a.HasField("capabilities", result, "capabilities")
for _, r := range a.All() {
if !r.Passed {
t.Errorf("assertion %q failed: %s", r.Name, r.Message)
}
}
}

func TestClientRPC_Streaming(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

ctx := context.Background()
if _, _, err := c.RPC(ctx, "initialize", map[string]any{
"protocolVersion": "2025-01-01",
"capabilities":    map[string]any{},
"clientInfo":      map[string]any{"name": "t", "version": "0"},
}); err != nil {
t.Fatalf("initialize: %v", err)
}

result, _, err := c.RPC(ctx, "session/new", map[string]any{"workDir": "/w"})
if err != nil {
t.Fatalf("session/new: %v", err)
}
var sr struct {
SessionID string `json:"sessionId"`
}
json.Unmarshal(result, &sr)
if sr.SessionID == "" {
t.Fatal("empty sessionId")
}

_, notifs, err := c.RPC(ctx, "session/prompt", map[string]any{
"sessionId": sr.SessionID,
"prompt":    "hello",
})
if err != nil {
t.Fatalf("session/prompt: %v", err)
}
if len(notifs) < 2 {
t.Errorf("expected >=2 delta notifications, got %d", len(notifs))
}
}

func TestClientRPC_UnknownMethod(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

_, _, err := c.RPC(context.Background(), "bogus/method", map[string]any{})
if err == nil {
t.Error("expected error for unknown method")
}
}

func TestScenarioHappyPath(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

result := ScenarioHappyPath(ctx, c)
assertScenarioPassed(t, result)
}

func TestScenarioMultiTurn(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

result := ScenarioMultiTurn(ctx, c)
assertScenarioPassed(t, result)
}

func TestScenarioCancel(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

result := ScenarioCancel(ctx, c)
assertScenarioPassed(t, result)
}

func TestScenarioSessionLoad(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

result := ScenarioSessionLoad(ctx, c)
assertScenarioPassed(t, result)
}

func TestScenarioErrorHandling(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

result := ScenarioErrorHandling(ctx, c)
assertScenarioPassed(t, result)
}

func TestScenarioMCPPassthrough(t *testing.T) {
addr, _ := startMiniServer(t)
c := makeTestClient(t, addr)
defer c.Close()

ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()

result := ScenarioMCPPassthrough(ctx, c)
assertScenarioPassed(t, result)
}

func TestAssertions(t *testing.T) {
a := &Assertions{}

raw := json.RawMessage(`{"key":"value","flag":true}`)
a.HasField("has key", raw, "key")
a.HasField("has flag", raw, "flag")
a.BoolField("flag is true", raw, "flag", true)
a.StringField("key is non-empty", raw, "key")
a.Check("always pass", true, "")

for _, r := range a.All() {
if !r.Passed {
t.Errorf("assertion %q unexpectedly failed: %s", r.Name, r.Message)
}
}

b := &Assertions{}
b.HasField("missing field", raw, "nonexistent")
if b.All()[0].Passed {
t.Error("expected HasField to fail for nonexistent key")
}
}

// ── helper ────────────────────────────────────────────────────────────────────

func assertScenarioPassed(t *testing.T, result ScenarioResult) {
t.Helper()
if !result.Passed {
for _, a := range result.Assertions {
if !a.Passed {
t.Errorf("scenario %q assertion %q: %s", result.Name, a.Name, a.Message)
}
}
if result.Details != "" {
t.Errorf("scenario %q: %s", result.Name, result.Details)
}
}
}
