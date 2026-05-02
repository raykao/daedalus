package main

import (
"bufio"
"context"
"encoding/json"
"net"
"strings"
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

if result["protocolVersion"] != float64(1) {
t.Errorf("expected protocolVersion 1, got %v", result["protocolVersion"])
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
result := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
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
nr := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
sid := nr["sessionId"].(string)

deltas, _, result := tc.sendPrompt(t, sid, "create hello.txt")
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
nr := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
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
nr := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
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
nr := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
sid := nr["sessionId"].(string)

_, serverReqs, result := tc.sendPrompt(t, sid, "create hello.txt")
if result["content"] == nil {
t.Error("expected content after permission flow")
}
if len(serverReqs) != 1 {
t.Fatalf("expected exactly 1 session/request_permission, got %d", len(serverReqs))
}
sr := serverReqs[0]
if sr["method"] != "session/request_permission" {
t.Errorf("expected method=session/request_permission, got %v", sr["method"])
}
params, ok := sr["params"].(map[string]any)
if !ok {
t.Fatalf("expected params to be a map, got %T", sr["params"])
}
toolCall, ok := params["toolCall"].(map[string]any)
if !ok {
t.Fatalf("expected toolCall to be a map, got %T", params["toolCall"])
}
if toolCall["tool"] != "bash" {
t.Errorf("expected toolCall.tool=bash, got %v", toolCall["tool"])
}
options, ok := params["options"].([]any)
if !ok {
t.Fatalf("expected options to be a slice, got %T", params["options"])
}
foundAllow := false
for _, o := range options {
om, ok := o.(map[string]any)
if !ok {
continue
}
if om["optionId"] == "allow_once" {
foundAllow = true
break
}
}
if !foundAllow {
t.Errorf("expected options to include optionId=allow_once, got %v", options)
}
}

func TestPermissionDenied(t *testing.T) {
cfg := fastCfg()
cfg.StreamingDelay = 5 * time.Millisecond
cfg.ResponseDelay = 5 * time.Millisecond
cfg.SendPermissions = true
addr := startTestServer(t, cfg)
tc := newTestClient(t, addr)
defer tc.close()

tc.sendRequest(t, "initialize", defaultInitParams())
nr := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
sid := nr["sessionId"].(string)

rpcErr := tc.sendPromptExpectError(t, sid, "create hello.txt", "deny")
if rpcErr == nil {
t.Fatal("expected RPC error when client denies permission")
}
if rpcErr.Code != ErrInternal {
t.Errorf("expected code=%d, got %d", ErrInternal, rpcErr.Code)
}
if !strings.Contains(rpcErr.Message, "deny") {
t.Errorf("expected error message to mention deny, got %q", rpcErr.Message)
}
}

// TestConn_WriteRequestAwaitResponse_UnblocksOnClose directly exercises the
// unit-under-test: a *conn whose Close() must wake any in-flight
// WriteRequestAwaitResponse callers far sooner than the per-call timeout.
//
// The previous integration-style test (TestConnectionCloseUnblocksAwaiter) only
// timed tc.c.Close(), which is a local TCP close that returns instantly even
// if the server-side handler is wedged on a 30s timer. It was a false positive:
// it passed regardless of whether close-cleanup actually ran. This unit test
// instead constructs a *conn over net.Pipe(), starts an awaiter, calls Close(),
// and asserts the awaiter returns within 200ms with the expected error shape.
func TestConn_WriteRequestAwaitResponse_UnblocksOnClose(t *testing.T) {
serverSide, clientSide := net.Pipe()
// Drain anything the server writes so WriteRequest can complete (net.Pipe
// is synchronous and unbuffered; without a reader the Write inside
// WriteRequest would block before the awaiter ever reaches its select).
go func() {
buf := make([]byte, 4096)
for {
if _, err := clientSide.Read(buf); err != nil {
return
}
}
}()
defer clientSide.Close()

c := newConn(serverSide)

type awaiterResult struct {
resp *Response
err  error
}
done := make(chan awaiterResult, 1)
go func() {
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
resp, err := c.WriteRequestAwaitResponse(ctx, "session/request_permission",
map[string]any{"sessionId": "x"}, 30*time.Second)
done <- awaiterResult{resp: resp, err: err}
}()

// Give the goroutine a moment to send the request and register a pending
// entry before we close.
time.Sleep(20 * time.Millisecond)

start := time.Now()
if err := c.Close(); err != nil {
// net.Pipe Close should not error; log for visibility but don't fail.
t.Logf("Close returned: %v", err)
}

select {
case r := <-done:
elapsed := time.Since(start)
if elapsed > 200*time.Millisecond {
t.Errorf("WriteRequestAwaitResponse took %v after Close (want <200ms)", elapsed)
}
// Two valid wakeup paths exist due to select non-determinism in
// WriteRequestAwaitResponse:
//   1. <-c.done fires first: the awaiter returns (nil, "connection closed").
//   2. The synthetic Response queued by Close()'s pending.Range is
//      received first: the awaiter returns (resp, nil) where
//      resp.Error has Code=ErrInternal, Message="connection closed".
// Both prove cleanup ran; accept either.
if r.err == nil {
if r.resp == nil || r.resp.Error == nil {
t.Fatalf("expected synthetic response with non-nil error, got %+v", r.resp)
}
if r.resp.Error.Code != ErrInternal {
t.Errorf("synthetic error code = %d, want ErrInternal (%d)", r.resp.Error.Code, ErrInternal)
}
if !strings.Contains(strings.ToLower(r.resp.Error.Message), "connection closed") {
t.Errorf("synthetic error message = %q, want contains 'connection closed'", r.resp.Error.Message)
}
} else {
if !strings.Contains(strings.ToLower(r.err.Error()), "connection closed") {
t.Errorf("error = %v, want contains 'connection closed'", r.err)
}
}

case <-time.After(2 * time.Second):
t.Fatal("WriteRequestAwaitResponse did not return within 2s of Close - cleanup is broken")
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
r := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
if r["sessionId"] == nil {
t.Fatalf("session %d should succeed", i+1)
}
}
_, rpcErr := tc.sendRequestRaw(t, "session/new", map[string]any{"cwd": "/workspace"})
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
nr := tc.sendRequest(t, "session/new", map[string]any{"cwd": "/workspace"})
sid := nr["sessionId"].(string)

_, rpcErr := tc.sendRequestRaw(t, "session/prompt", map[string]any{
"sessionId": sid,
"prompt":    []map[string]any{{"type": "text", "text": "hello"}},
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

// sendPrompt writes a session/prompt request, auto-approves any
// session/request_permission server-requests with optionId=allow_once, and
// returns the notifications, server-requests it observed, and the final
// result.
func (tc *testClient) sendPrompt(t *testing.T, sessionID, prompt string) ([]map[string]any, []map[string]any, map[string]any) {
t.Helper()
return tc.sendPromptWithPermission(t, sessionID, prompt, "allow_once")
}

// sendPromptWithPermission is like sendPrompt but lets the test choose which
// optionId to return for permission requests (e.g. "deny").
func (tc *testClient) sendPromptWithPermission(t *testing.T, sessionID, prompt, optionID string) ([]map[string]any, []map[string]any, map[string]any) {
t.Helper()
id := tc.nextID()
if err := tc.c.WriteMessage(Request{
JSONRPC: "2.0",
ID:      &id,
Method:  "session/prompt",
Params: mustMarshal(t, map[string]any{
"sessionId": sessionID,
"prompt":    []map[string]any{{"type": "text", "text": prompt}},
}),
}); err != nil {
t.Fatalf("write prompt: %v", err)
}

var notifications []map[string]any
var serverRequests []map[string]any
for tc.scanner.Scan() {
var raw map[string]json.RawMessage
if err := json.Unmarshal(tc.scanner.Bytes(), &raw); err != nil {
t.Fatalf("parse message: %v", err)
}
_, hasID := raw["id"]
_, hasMethod := raw["method"]
// Server-to-client request (has both id and method): respond and continue.
if hasID && hasMethod {
var inReq Request
if err := json.Unmarshal(tc.scanner.Bytes(), &inReq); err != nil {
t.Fatalf("parse server request: %v", err)
}
if inReq.Method == "session/request_permission" {
var params map[string]any
if err := json.Unmarshal(inReq.Params, &params); err != nil {
t.Fatalf("parse server-request params: %v", err)
}
serverRequests = append(serverRequests, map[string]any{
"method": inReq.Method,
"params": params,
})
_ = tc.c.WriteMessage(map[string]any{
"jsonrpc": "2.0",
"id":      inReq.ID,
"result": map[string]any{
"outcome": map[string]any{"optionId": optionID},
},
})
continue
}
// Unknown server request: send method-not-found.
_ = tc.c.WriteMessage(map[string]any{
"jsonrpc": "2.0",
"id":      inReq.ID,
"error":   map[string]any{"code": -32601, "message": "method not found"},
})
continue
}
// Notification (method but no id).
if !hasID && hasMethod {
var params map[string]any
if p, ok := raw["params"]; ok {
_ = json.Unmarshal(p, &params)
}
notifications = append(notifications, params)
continue
}
// Response to our prompt (id, no method).
if errRaw, ok := raw["error"]; ok && string(errRaw) != "null" {
var rpcErr RPCError
_ = json.Unmarshal(errRaw, &rpcErr)
t.Fatalf("prompt error: %s", rpcErr.Message)
}
var result map[string]any
_ = json.Unmarshal(raw["result"], &result)
return notifications, serverRequests, result
}
t.Fatal("connection closed before prompt response")
return nil, nil, nil
}

// sendPromptExpectError sends a prompt, auto-responds to permission requests
// with the given optionId, and expects the final response to be an RPC error.
func (tc *testClient) sendPromptExpectError(t *testing.T, sessionID, prompt, optionID string) *RPCError {
t.Helper()
id := tc.nextID()
if err := tc.c.WriteMessage(Request{
JSONRPC: "2.0",
ID:      &id,
Method:  "session/prompt",
Params: mustMarshal(t, map[string]any{
"sessionId": sessionID,
"prompt":    []map[string]any{{"type": "text", "text": prompt}},
}),
}); err != nil {
t.Fatalf("write prompt: %v", err)
}
for tc.scanner.Scan() {
var raw map[string]json.RawMessage
if err := json.Unmarshal(tc.scanner.Bytes(), &raw); err != nil {
t.Fatalf("parse message: %v", err)
}
_, hasID := raw["id"]
_, hasMethod := raw["method"]
if hasID && hasMethod {
var inReq Request
if err := json.Unmarshal(tc.scanner.Bytes(), &inReq); err != nil {
t.Fatalf("parse server request: %v", err)
}
if inReq.Method == "session/request_permission" {
_ = tc.c.WriteMessage(map[string]any{
"jsonrpc": "2.0",
"id":      inReq.ID,
"result": map[string]any{
"outcome": map[string]any{"optionId": optionID},
},
})
continue
}
_ = tc.c.WriteMessage(map[string]any{
"jsonrpc": "2.0",
"id":      inReq.ID,
"error":   map[string]any{"code": -32601, "message": "method not found"},
})
continue
}
if !hasID && hasMethod {
continue // notification
}
if errRaw, ok := raw["error"]; ok && string(errRaw) != "null" {
var rpcErr RPCError
_ = json.Unmarshal(errRaw, &rpcErr)
return &rpcErr
}
t.Fatal("expected error response, got success")
return nil
}
t.Fatal("connection closed before prompt response")
return nil
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
"protocolVersion": 1,
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
