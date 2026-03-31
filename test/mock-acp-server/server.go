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
	mu  sync.Mutex
	enc *json.Encoder
}

func newConn(c net.Conn) *conn {
	return &conn{Conn: c, enc: json.NewEncoder(c)}
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
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
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
		s.handler.Dispatch(ctx, c, &req)
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("connection closed", "err", err)
	}
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
