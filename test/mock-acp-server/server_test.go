package main

import (
"bufio"
"context"
"encoding/json"
"net"
"testing"
"time"
)

// ── server bootstrapping helpers ──────────────────────────────────────────────

func startTestServer(t *testing.T, cfg MockConfig) string {
t.Helper()
cfg.Port = 0
srv := NewServer(cfg)
if err := srv.Listen(); err != nil {
t.Fatalf("listen: %v", err)
}
addr := srv.Addr().String()
ctx, cancel := context.WithCancel(context.Background())
t.Cleanup(func() {
cancel()
time.Sleep(50 * time.Millisecond)
})
go srv.Serve(ctx) //nolint:errcheck
waitForTCP(t, addr, 2*time.Second)
return addr
}

func waitForTCP(t *testing.T, addr string, timeout time.Duration) {
t.Helper()
deadline := time.Now().Add(timeout)
for time.Now().Before(deadline) {
c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
if err == nil {
c.Close()
return
}
time.Sleep(20 * time.Millisecond)
}
t.Fatalf("server %s did not become ready within %s", addr, timeout)
}

// ── test cases ────────────────────────────────────────────────────────────────

func TestInitialize(t *testing.T) {
addr := startTestServer(t, fastCfg())
tc := newTestClient(t, addr)
defer tc.close()

result := tc.sendRequest(t, "initialize", defaultInitParams())

if result["protocolVersion"] != "2025-01-01" {
t.Errorf("expected protocolVersion 2025-01-01, got %v", result["protocolVersion"])
}
caps, _ := result["capabilities"].(map[string]any)
if caps == nil {
t.Fatal("missing capabilities")
}
if caps["streaming"] != true {
t.Error("expected streaming=true")
}
}

func TestSessionNew(t *testing.T) {
addr := startTestServer(t, fastCfg())
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
result := tc.sendRequest(t, "session/new", map[string]any{"workDir": "/workspace"})
if result["sessionId"] == "" {
t.Error("expected non-empty sessionId")
}
}

func TestSessionPromptStreaming(t *testing.T) {
cfg := fastCfg()
cfg.StreamingDelay = 10 * time.Millisecond
cfg.ResponseDelay = 10 * time.Millisecond
addr := startTestServer(t, cfg)
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
nr := tc.sendRequest(t, "session/new", map[string]any{"workDir": "/workspace"})
sid := nr["sessionId"].(string)

deltas, result := tc.sendPrompt(t, sid, "create hello.txt")
if len(deltas) < 2 {
t.Errorf("expected ≥2 message_delta notifications, got %d", len(deltas))
}
if result["content"] == nil {
t.Error("expected content in prompt result")
}
}

func TestSessionCancel(t *testing.T) {
addr := startTestServer(t, fastCfg())
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
nr := tc.sendRequest(t, "session/new", map[string]any{"workDir": "/workspace"})
sid := nr["sessionId"].(string)

result := tc.sendRequest(t, "session/cancel", map[string]any{"sessionId": sid})
if result["canceled"] != true {
t.Errorf("expected canceled=true, got %v", result["canceled"])
}
}

func TestSessionLoad(t *testing.T) {
cfg := fastCfg()
cfg.StreamingDelay = 5 * time.Millisecond
cfg.ResponseDelay = 5 * time.Millisecond
addr := startTestServer(t, cfg)
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
nr := tc.sendRequest(t, "session/new", map[string]any{"workDir": "/workspace"})
sid := nr["sessionId"].(string)

tc.sendPrompt(t, sid, "hello")

result := tc.sendRequest(t, "session/load", map[string]any{"sessionId": sid})
if result["loaded"] != true {
t.Errorf("expected loaded=true, got %v", result["loaded"])
}
}

func TestPermissionRequest(t *testing.T) {
cfg := fastCfg()
cfg.StreamingDelay = 5 * time.Millisecond
cfg.ResponseDelay = 5 * time.Millisecond
cfg.SendPermissions = true
addr := startTestServer(t, cfg)
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
nr := tc.sendRequest(t, "session/new", map[string]any{"workDir": "/workspace"})
sid := nr["sessionId"].(string)

_, result := tc.sendPrompt(t, sid, "create hello.txt")
if result["content"] == nil {
t.Error("expected content after permission flow")
}
}

func TestMaxSessions(t *testing.T) {
cfg := fastCfg()
cfg.MaxSessions = 2
addr := startTestServer(t, cfg)
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
for i := 0; i < 2; i++ {
r := tc.sendRequest(t, "session/new", map[string]any{"workDir": "/workspace"})
if r["sessionId"] == nil {
t.Fatalf("session %d should succeed", i+1)
}
}
_, rpcErr := tc.sendRequestRaw(t, "session/new", map[string]any{"workDir": "/workspace"})
if rpcErr == nil {
t.Error("expected error when max sessions exceeded")
}
}

func TestUnknownMethod(t *testing.T) {
addr := startTestServer(t, fastCfg())
tc := newTestClient(t, addr)
defer tc.close()

_, rpcErr := tc.sendRequestRaw(t, "nonexistent/method", map[string]any{})
if rpcErr == nil {
t.Error("expected method-not-found error")
}
}

func TestFailOnPrompt(t *testing.T) {
cfg := fastCfg()
cfg.FailOnPrompt = true
addr := startTestServer(t, cfg)
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
nr := tc.sendRequest(t, "session/new", map[string]any{"workDir": "/workspace"})
sid := nr["sessionId"].(string)

_, rpcErr := tc.sendRequestRaw(t, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    "hello",
})
if rpcErr == nil {
t.Error("expected error from FailOnPrompt")
}
}

// ── test client ───────────────────────────────────────────────────────────────

type testClient struct {
c       *conn
scanner *bufio.Scanner
}

func newTestClient(t *testing.T, addr string) *testClient {
t.Helper()
c, err := net.Dial("tcp", addr)
if err != nil {
t.Fatalf("dial %s: %v", addr, err)
}
wc := newConn(c)
return &testClient{c: wc, scanner: bufio.NewScanner(wc)}
}

func (tc *testClient) close() { tc.c.Close() }

var reqCounter int64

func (tc *testClient) nextID() int64 {
reqCounter++
return reqCounter
}

func (tc *testClient) sendRequest(t *testing.T, method string, params any) map[string]any {
t.Helper()
result, rpcErr := tc.sendRequestRaw(t, method, params)
if rpcErr != nil {
t.Fatalf("RPC error for %s: code=%d msg=%s", method, rpcErr.Code, rpcErr.Message)
}
return result
}

func (tc *testClient) sendRequestRaw(t *testing.T, method string, params any) (map[string]any, *RPCError) {
t.Helper()
id := tc.nextID()
if err := tc.c.WriteMessage(Request{
JSONRPC: "2.0",
ID:      &id,
Method:  method,
Params:  mustMarshal(t, params),
}); err != nil {
t.Fatalf("write request %s: %v", method, err)
}
return tc.readResponseSkipNotifications(t)
}

// sendPrompt writes a session/prompt request and collects all notifications
// until the final response arrives.
func (tc *testClient) sendPrompt(t *testing.T, sessionID, prompt string) ([]map[string]any, map[string]any) {
t.Helper()
id := tc.nextID()
if err := tc.c.WriteMessage(Request{
JSONRPC: "2.0",
ID:      &id,
Method:  "session/prompt",
Params: mustMarshal(t, map[string]any{
"sessionId": sessionID,
"prompt":    prompt,
}),
}); err != nil {
t.Fatalf("write prompt: %v", err)
}

var notifications []map[string]any
for tc.scanner.Scan() {
var raw map[string]json.RawMessage
if err := json.Unmarshal(tc.scanner.Bytes(), &raw); err != nil {
t.Fatalf("parse message: %v", err)
}
if _, hasID := raw["id"]; !hasID {
var params map[string]any
if p, ok := raw["params"]; ok {
_ = json.Unmarshal(p, &params)
}
notifications = append(notifications, params)
continue
}
if errRaw, ok := raw["error"]; ok && string(errRaw) != "null" {
var rpcErr RPCError
_ = json.Unmarshal(errRaw, &rpcErr)
t.Fatalf("prompt error: %s", rpcErr.Message)
}
var result map[string]any
_ = json.Unmarshal(raw["result"], &result)
return notifications, result
}
t.Fatal("connection closed before prompt response")
return nil, nil
}

// readResponseSkipNotifications reads lines until a message with an id field is found.
func (tc *testClient) readResponseSkipNotifications(t *testing.T) (map[string]any, *RPCError) {
t.Helper()
for tc.scanner.Scan() {
var raw map[string]json.RawMessage
if err := json.Unmarshal(tc.scanner.Bytes(), &raw); err != nil {
t.Fatalf("parse response: %v", err)
}
if _, hasID := raw["id"]; !hasID {
continue // notification — skip
}
if errRaw, ok := raw["error"]; ok && string(errRaw) != "null" {
var rpcErr RPCError
if err := json.Unmarshal(errRaw, &rpcErr); err != nil {
t.Fatalf("parse error field: %v", err)
}
return nil, &rpcErr
}
var result map[string]any
if err := json.Unmarshal(raw["result"], &result); err != nil {
t.Fatalf("parse result: %v", err)
}
return result, nil
}
t.Fatal("connection closed before response")
return nil, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustMarshal(t *testing.T, v any) json.RawMessage {
t.Helper()
b, err := json.Marshal(v)
if err != nil {
t.Fatalf("marshal: %v", err)
}
return b
}

func defaultInitParams() map[string]any {
return map[string]any{
"protocolVersion": "2025-01-01",
"capabilities":    map[string]any{"streaming": true},
"clientInfo":      map[string]any{"name": "test", "version": "0.0.1"},
}
}

func fastCfg() MockConfig {
cfg := DefaultConfig()
cfg.StreamingDelay = 0
cfg.ResponseDelay = 0
return cfg
}
