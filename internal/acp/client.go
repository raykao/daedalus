package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultDialTimeout  = 10 * time.Second
	defaultWriteTimeout = 5 * time.Second
	protocolVersion     = 1
)

// Client is an ACP client that speaks JSON-RPC 2.0 over TCP
type Client struct {
	addr    string
	conn    net.Conn
	writer  *bufio.Writer
	mu      sync.Mutex // protects writes
	nextID  atomic.Int64
	pending sync.Map // map[int64]chan *Response
	logger  *slog.Logger
	cancel  context.CancelFunc
}

// NewClient creates a new ACP client connected to addr
func NewClient(addr string, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		addr:   addr,
		logger: logger,
	}
}

// Connect dials the ACP agent and starts the reader loop
func (c *Client) Connect(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("acp: dial %s: %w", c.addr, err)
	}
	c.conn = conn
	c.writer = bufio.NewWriter(conn)

	// Use context.Background() — the readLoop must outlive the connect context.
	// It is stopped only when Close() calls c.cancel().
	readerCtx, readerCancel := context.WithCancel(context.Background())
	c.cancel = readerCancel

	go c.readLoop(readerCtx)
	return nil
}

// Close shuts down the client connection
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Initialize performs ACP capability negotiation
func (c *Client) Initialize(ctx context.Context) (*InitializeResult, error) {
	params := InitializeParams{
		ProtocolVersion: protocolVersion,
		Capabilities:    ClientCapabilities{Streaming: true},
		ClientInfo:      ClientInfo{Name: "daedalus-proxy", Version: "0.1.0"},
	}
	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return nil, fmt.Errorf("acp: initialize: %w", err)
	}
	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("acp: initialize: unmarshal result: %w", err)
	}
	return &result, nil
}

// NewSession creates a new ACP session and returns the session ID
func (c *Client) NewSession(ctx context.Context, workDir string) (string, error) {
	params := SessionNewParams{
		WorkDir:    workDir,
		MCPServers: []interface{}{},
	}
	resp, err := c.call(ctx, "session/new", params)
	if err != nil {
		return "", fmt.Errorf("acp: session/new: %w", err)
	}
	var result SessionNewResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return "", fmt.Errorf("acp: session/new: unmarshal result: %w", err)
	}
	return result.SessionID, nil
}

// Prompt sends a prompt to the agent and collects the full response (including streaming deltas)
func (c *Client) Prompt(ctx context.Context, sessionID, prompt string) (string, error) {
	params := SessionPromptParams{
		SessionID: sessionID,
		Prompt:    []PromptPart{{Type: "text", Text: prompt}},
	}

	// Register a delta collector before sending the request
	id := c.nextID.Add(1)
	replyCh := make(chan *Response, 1)
	c.pending.Store(id, replyCh)

	// Also register a delta accumulator keyed by session
	deltaCh := make(chan string, 256)
	deltaKey := "delta:" + sessionID
	c.pending.Store(deltaKey, deltaCh)
	defer c.pending.Delete(deltaKey)

	if err := c.sendRequest(ctx, id, "session/prompt", params); err != nil {
		c.pending.Delete(id)
		return "", fmt.Errorf("acp: session/prompt: send: %w", err)
	}

	var deltas []string
	for {
		select {
		case <-ctx.Done():
			c.pending.Delete(id)
			return "", ctx.Err()
		case delta := <-deltaCh:
			deltas = append(deltas, delta)
		case resp := <-replyCh:
			// Drain remaining deltas
			done := false
			for !done {
				select {
				case delta := <-deltaCh:
					deltas = append(deltas, delta)
				default:
					done = true
				}
			}
			if resp.Error != nil {
				return "", fmt.Errorf("acp: session/prompt: rpc error %d: %s", resp.Error.Code, resp.Error.Message)
			}
			var result SessionPromptResult
			if err := json.Unmarshal(resp.Result, &result); err != nil {
				// If we have accumulated deltas, use them as the content
				if len(deltas) > 0 {
					return strings.Join(deltas, ""), nil
				}
				return "", fmt.Errorf("acp: session/prompt: unmarshal result: %w", err)
			}
			// Prefer the final result content; fall back to accumulated deltas
			if result.Content != "" {
				return result.Content, nil
			}
			return strings.Join(deltas, ""), nil
		}
	}
}

// CancelSession sends a cancel for the given session
func (c *Client) CancelSession(ctx context.Context, sessionID string) error {
	params := SessionCancelParams{SessionID: sessionID}
	_, err := c.call(ctx, "session/cancel", params)
	return err
}

// call is the low-level helper: allocates an ID, sends, waits for response
func (c *Client) call(ctx context.Context, method string, params interface{}) (*Response, error) {
	id := c.nextID.Add(1)
	replyCh := make(chan *Response, 1)
	c.pending.Store(id, replyCh)
	defer c.pending.Delete(id)

	if err := c.sendRequest(ctx, id, method, params); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-replyCh:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp, nil
	}
}

// sendRequest serializes and writes a JSON-RPC request to the wire
func (c *Client) sendRequest(ctx context.Context, id int64, method string, params interface{}) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}

	req := Request{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  rawParams,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.SetWriteDeadline(time.Now().Add(defaultWriteTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := c.writer.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return c.writer.Flush()
}

// readLoop continuously reads NDJSON from the TCP connection and dispatches messages
func (c *Client) readLoop(ctx context.Context) {
	scanner := bufio.NewScanner(c.conn)
	// Increase scanner buffer for large responses
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, len(buf))

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				c.logger.Error("acp: read loop scanner error", "err", err)
			}
			// Connection closed — fail all pending
			c.pending.Range(func(key, value any) bool {
				if ch, ok := value.(chan *Response); ok {
					ch <- &Response{Error: &RPCError{Code: -32000, Message: "connection closed"}}
				}
				return true
			})
			return
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg Response
		if err := json.Unmarshal(line, &msg); err != nil {
			c.logger.Warn("acp: failed to parse message", "raw", string(line), "err", err)
			continue
		}

		c.dispatch(&msg)
	}
}

// dispatch routes a parsed message to the appropriate channel
func (c *Client) dispatch(msg *Response) {
	// Server-to-client request: has both method and non-nil ID.
	// Must be answered with a proper JSON-RPC response.
	if msg.IsServerRequest() {
		c.handleServerRequest(msg)
		return
	}
	if msg.IsNotification() {
		c.handleNotification(msg)
		return
	}

	if msg.ID == nil {
		c.logger.Warn("acp: received message with no id and no method", "msg", msg)
		return
	}

	if ch, ok := c.pending.LoadAndDelete(*msg.ID); ok {
		if replyCh, ok := ch.(chan *Response); ok {
			replyCh <- msg
		}
	} else {
		c.logger.Warn("acp: received response for unknown id", "id", *msg.ID)
	}
}

// handleServerRequest responds to server-to-client JSON-RPC requests.
// Unlike notifications, these have a non-nil ID and require a response.
func (c *Client) handleServerRequest(msg *Response) {
	switch msg.Method {
	case "session/request_permission":
		var params PermissionRequestParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			c.logger.Warn("acp: failed to parse request_permission params", "err", err)
			return
		}
		c.logger.Info("acp: auto-approving permission request", "sessionId", params.SessionID)
		reqID := *msg.ID
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := PermissionApprovalResult{
				Outcome: PermissionOutcome{OptionID: "allow_once"},
			}
			if err := c.sendResponse(ctx, reqID, result); err != nil {
				c.logger.Error("acp: failed to send permission approval", "err", err)
			}
		}()
	default:
		c.logger.Debug("acp: unhandled server request", "method", msg.Method)
	}
}

// handleNotification routes server-sent notifications
func (c *Client) handleNotification(msg *Response) {
	switch msg.Method {
	case "session/update":
		var params SessionUpdateParams
		if err := json.Unmarshal(msg.Params, &params); err != nil {
			c.logger.Warn("acp: failed to parse session/update params", "err", err)
			return
		}
		// Only agent_message_chunk carries user-visible response text.
		// agent_thought_chunk is internal extended thinking; skip it.
		if params.Update.SessionUpdate == "agent_message_chunk" && params.Update.Content.Text != "" {
			deltaKey := "delta:" + params.SessionID
			if ch, ok := c.pending.Load(deltaKey); ok {
				if deltaCh, ok := ch.(chan string); ok {
					select {
					case deltaCh <- params.Update.Content.Text:
					default:
						c.logger.Warn("acp: delta channel full, dropping message chunk", "sessionId", params.SessionID)
					}
				}
			}
		}
	default:
		c.logger.Debug("acp: unhandled notification", "method", msg.Method)
	}
}

// sendResponse sends a JSON-RPC response for a server-to-client request.
func (c *Client) sendResponse(ctx context.Context, id int64, result interface{}) error {
	data, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.conn.SetWriteDeadline(time.Now().Add(defaultWriteTimeout)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if _, err := c.writer.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return c.writer.Flush()
}
