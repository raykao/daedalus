package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Message types per JSON-RPC 2.0.

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      *int64        `json:"id,omitempty"`
	Result  any           `json:"result,omitempty"`
	Error   *RPCError     `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

// Standard JSON-RPC error codes.
const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternal       = -32603
)

// conn wraps a network connection with a mutex-protected writer.
type conn struct {
	net.Conn
	mu        sync.Mutex
	enc       *json.Encoder
	nextID    atomic.Int64
	pending   sync.Map // map[int64]chan *Response
	done      chan struct{}
	closeOnce sync.Once
}

func newConn(c net.Conn) *conn {
	return &conn{Conn: c, enc: json.NewEncoder(c), done: make(chan struct{})}
}

// Close tears down the connection, signalling any goroutines blocked in
// WriteRequestAwaitResponse so they don't sit on the 30s timeout. Idempotent.
func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		// Best-effort: wake any pending awaiters with a synthetic error
		// response. The non-blocking send is safe because each pending
		// channel has capacity 1 and only one consumer.
		c.pending.Range(func(k, v any) bool {
			if ch, ok := v.(chan *Response); ok {
				select {
				case ch <- &Response{JSONRPC: "2.0", Error: &RPCError{Code: ErrInternal, Message: "connection closed"}}:
				default:
				}
			}
			c.pending.Delete(k)
			return true
		})
	})
	return c.Conn.Close()
}

// allocRequestID returns the next outgoing server-to-client request ID.
func (c *conn) allocRequestID() int64 {
	return c.nextID.Add(1)
}

// registerPending registers a reply channel for an outgoing request ID.
func (c *conn) registerPending(id int64) chan *Response {
	ch := make(chan *Response, 1)
	c.pending.Store(id, ch)
	return ch
}

// deletePending removes the reply channel for an outgoing request ID.
func (c *conn) deletePending(id int64) {
	c.pending.Delete(id)
}

// resolvePending delivers a response to the registered channel, if any.
func (c *conn) resolvePending(id int64, resp *Response) bool {
	if ch, ok := c.pending.LoadAndDelete(id); ok {
		if replyCh, ok := ch.(chan *Response); ok {
			replyCh <- resp
			return true
		}
	}
	return false
}

// WriteRequest sends a JSON-RPC request (with id) without waiting for a reply.
func (c *conn) WriteRequest(id int64, method string, params any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.WriteMessage(Request{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
		Params:  rawParams,
	})
}

// WriteRequestAwaitResponse sends a server-to-client JSON-RPC request and
// blocks until the matching response is received, ctx is cancelled, or the
// timeout elapses.
func (c *conn) WriteRequestAwaitResponse(ctx context.Context, method string, params any, timeout time.Duration) (*Response, error) {
	id := c.allocRequestID()
	ch := c.registerPending(id)
	defer c.deletePending(id)

	if err := c.WriteRequest(id, method, params); err != nil {
		return nil, err
	}

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timeoutCh = t.C
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("connection closed")
	case <-timeoutCh:
		return nil, fmt.Errorf("timeout waiting for response to %s", method)
	case resp := <-ch:
		return resp, nil
	}
}

// WriteMessage serialises v as NDJSON (newline-delimited JSON) to the connection.
func (c *conn) WriteMessage(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enc.Encode(v)
}

// WriteResponse sends a JSON-RPC response.
func (c *conn) WriteResponse(id *int64, result any, rpcErr *RPCError) error {
	return c.WriteMessage(Response{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
		Error:   rpcErr,
	})
}

// WriteNotification sends a JSON-RPC notification (no id).
func (c *conn) WriteNotification(method string, params any) error {
	return c.WriteMessage(Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	})
}

// Server is the mock ACP TCP server.
type Server struct {
	cfg      MockConfig
	listener net.Listener
	handler  *Handler

	mu       sync.Mutex
	sessions map[string]*Session
}

// NewServer creates a Server with the given configuration.
func NewServer(cfg MockConfig) *Server {
	s := &Server{
		cfg:      cfg,
		sessions: make(map[string]*Session),
	}
	s.handler = NewHandler(s)
	return s
}

// Listen binds to the configured TCP port.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.cfg.Port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = ln
	slog.Info("mock ACP server listening", "addr", ln.Addr())
	return nil
}

// Addr returns the listener address (useful when port 0 is used in tests).
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// Serve accepts connections until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.listener.Close()
	}()

	for {
		c, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		slog.Debug("new connection", "remote", c.RemoteAddr())
		go s.handleConn(ctx, newConn(c))
	}
}

// handleConn reads NDJSON lines and dispatches to the handler.
func (s *Server) handleConn(ctx context.Context, c *conn) {
	defer c.Close()
	scanner := bufio.NewScanner(c)
	// Allow large messages.
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, len(buf))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Detect responses to server-to-client requests: have an id but no method.
		var probe struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *RPCError       `json:"error"`
		}
		if err := json.Unmarshal(line, &probe); err == nil && probe.Method == "" && probe.ID != nil {
			c.resolvePending(*probe.ID, &Response{
				JSONRPC: "2.0",
				ID:      probe.ID,
				Result:  rawAny(probe.Result),
				Error:   probe.Error,
			})
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			slog.Warn("parse error", "err", err)
			_ = c.WriteResponse(nil, nil, &RPCError{Code: ErrParseError, Message: "parse error"})
			continue
		}
		if req.JSONRPC != "2.0" {
			_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidRequest, Message: "invalid jsonrpc version"})
			continue
		}
		slog.Debug("received request", "method", req.Method, "id", req.ID)
		// Dispatch each request in its own goroutine so a long-running
		// handler (e.g. one that issues a server-to-client request and
		// waits for the response) doesn't stall the read loop.
		go s.handler.Dispatch(ctx, c, &req)
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("connection closed", "err", err)
	}
}

// rawAny converts a json.RawMessage to a non-nil any so that the Response
// struct's pending consumer can re-marshal it without losing structure.
func rawAny(r json.RawMessage) any {
	if len(r) == 0 {
		return nil
	}
	return r
}

// addSession stores a session (thread-safe).
func (s *Server) addSession(sess *Session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sessions) >= s.cfg.MaxSessions {
		return false
	}
	s.sessions[sess.ID] = sess
	return true
}

// getSession retrieves a session by ID.
func (s *Server) getSession(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// Session stores per-session state.
type Session struct {
	ID      string
	WorkDir string
	History []HistoryEntry
	mu      sync.Mutex
}

// HistoryEntry captures a single prompt/response turn.
type HistoryEntry struct {
	Prompt   string
	Response string
}

// AppendHistory adds a turn to the session history.
func (sess *Session) AppendHistory(prompt, response string) {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	sess.History = append(sess.History, HistoryEntry{Prompt: prompt, Response: response})
}
