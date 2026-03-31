package main

import (
"bufio"
"context"
"encoding/json"
"fmt"
"net"
"sync/atomic"
"time"
)

// ── JSON-RPC types ────────────────────────────────────────────────────────────

type Request struct {
JSONRPC string          `json:"jsonrpc"`
ID      *int64          `json:"id,omitempty"`
Method  string          `json:"method"`
Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
JSONRPC string          `json:"jsonrpc"`
ID      *int64          `json:"id,omitempty"`
Result  json.RawMessage `json:"result,omitempty"`
Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
Code    int    `json:"code"`
Message string `json:"message"`
}

// Notification is a JSON-RPC message with no id.
type Notification struct {
JSONRPC string          `json:"jsonrpc"`
Method  string          `json:"method"`
Params  json.RawMessage `json:"params,omitempty"`
}

// Message is a union of Notification and Response.
type Message struct {
IsNotification bool
Notification   *Notification
Response       *Response
}

// ── Client ────────────────────────────────────────────────────────────────────

var idCounter int64

func nextID() int64 { return atomic.AddInt64(&idCounter, 1) }

// Client is a low-level ACP TCP connection.
type Client struct {
conn    net.Conn
scanner *bufio.Scanner
enc     *json.Encoder
verbose bool
}

// Dial opens a TCP connection to addr with the given connect timeout.
func Dial(addr string, timeout time.Duration) (*Client, error) {
c, err := net.DialTimeout("tcp", addr, timeout)
if err != nil {
return nil, fmt.Errorf("dial %s: %w", addr, err)
}
return &Client{
conn:    c,
scanner: bufio.NewScanner(c),
enc:     json.NewEncoder(c),
}, nil
}

// Close closes the underlying connection.
func (c *Client) Close() { c.conn.Close() }

// Send serialises and writes a JSON-RPC request, returning the request ID.
func (c *Client) Send(method string, params any) (int64, error) {
id := nextID()
req := Request{
JSONRPC: "2.0",
ID:      &id,
Method:  method,
}
if params != nil {
b, err := json.Marshal(params)
if err != nil {
return 0, err
}
req.Params = b
}
if c.verbose {
b, _ := json.Marshal(req)
fmt.Printf("  → %s\n", b)
}
return id, c.enc.Encode(req)
}

// ReadMessage reads the next NDJSON line and parses it.
func (c *Client) ReadMessage(ctx context.Context) (*Message, error) {
type readResult struct {
msg *Message
err error
}
ch := make(chan readResult, 1)
go func() {
if !c.scanner.Scan() {
if err := c.scanner.Err(); err != nil {
ch <- readResult{err: err}
} else {
ch <- readResult{err: fmt.Errorf("connection closed")}
}
return
}
line := c.scanner.Bytes()
if c.verbose {
fmt.Printf("  ← %s\n", line)
}
var raw map[string]json.RawMessage
if err := json.Unmarshal(line, &raw); err != nil {
ch <- readResult{err: fmt.Errorf("parse: %w", err)}
return
}
if _, hasID := raw["id"]; !hasID {
var n Notification
if err := json.Unmarshal(line, &n); err != nil {
ch <- readResult{err: fmt.Errorf("parse notification: %w", err)}
return
}
ch <- readResult{msg: &Message{IsNotification: true, Notification: &n}}
return
}
var resp Response
if err := json.Unmarshal(line, &resp); err != nil {
ch <- readResult{err: fmt.Errorf("parse response: %w", err)}
return
}
ch <- readResult{msg: &Message{Response: &resp}}
}()

select {
case <-ctx.Done():
return nil, ctx.Err()
case r := <-ch:
return r.msg, r.err
}
}

// RPC sends a request and collects all notifications until the response arrives.
func (c *Client) RPC(ctx context.Context, method string, params any) (json.RawMessage, []Notification, error) {
if _, err := c.Send(method, params); err != nil {
return nil, nil, fmt.Errorf("send %s: %w", method, err)
}
var notifs []Notification
for {
msg, err := c.ReadMessage(ctx)
if err != nil {
return nil, notifs, fmt.Errorf("read response for %s: %w", method, err)
}
if msg.IsNotification {
notifs = append(notifs, *msg.Notification)
continue
}
if msg.Response.Error != nil {
return nil, notifs, fmt.Errorf("RPC error %d: %s", msg.Response.Error.Code, msg.Response.Error.Message)
}
return msg.Response.Result, notifs, nil
}
}
