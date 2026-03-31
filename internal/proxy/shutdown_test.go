package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/raykao/agent-forge/internal/acp"
)

// slowACPServer is an in-process TCP mock that handles ACP requests
// concurrently - one goroutine per request - so session/cancel can arrive
// while session/prompt is still blocking.
type slowACPServer struct {
	listener     net.Listener
	promptBlock  chan struct{} // close to unblock all pending prompt handlers
	promptReady  chan struct{} // buffered(1); receives when server is about to block on prompt

	mu          sync.Mutex
	cancelCalls []string // session IDs that received session/cancel
}

// startSlowACPServer starts the mock and registers cleanup with t.
func startSlowACPServer(t *testing.T) *slowACPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("slow ACP server: listen: %v", err)
	}
	s := &slowACPServer{
		listener:    ln,
		promptBlock: make(chan struct{}),
		promptReady: make(chan struct{}, 1),
	}
	go s.serve()
	t.Cleanup(func() {
		ln.Close()
		// Unblock any goroutines still waiting so they can exit cleanly.
		select {
		case <-s.promptBlock:
		default:
			close(s.promptBlock)
		}
	})
	return s
}

func (s *slowACPServer) addr() string {
	return s.listener.Addr().String()
}

func (s *slowACPServer) cancelCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cancelCalls)
}

func (s *slowACPServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *slowACPServer) handleConn(conn net.Conn) {
	defer conn.Close()

	var writeMu sync.Mutex
	writeJSON := func(v interface{}) {
		data, _ := json.Marshal(v)
		data = append(data, '\n')
		writeMu.Lock()
		defer writeMu.Unlock()
		conn.Write(data) //nolint:errcheck
	}

	var sessionSeq atomic.Int64

	scanner := bufio.NewScanner(conn)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, len(buf))

	for scanner.Scan() {
		var req acp.Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		// Copy req so the goroutine captures its own value.
		r := req
		go func() {
			switch r.Method {
			case "initialize":
				result := acp.InitializeResult{
					ProtocolVersion: "2025-01-01",
					Capabilities:    acp.ServerCapabilities{Streaming: true},
					ServerInfo:      acp.ServerInfo{Name: "slow-mock", Version: "1.0.0"},
				}
				raw, _ := json.Marshal(result)
				writeJSON(acp.Response{JSONRPC: "2.0", ID: r.ID, Result: raw})

			case "session/new":
				id := sessionSeq.Add(1)
				result := acp.SessionNewResult{SessionID: fmt.Sprintf("sess-%d", id)}
				raw, _ := json.Marshal(result)
				writeJSON(acp.Response{JSONRPC: "2.0", ID: r.ID, Result: raw})

			case "session/prompt":
				var params acp.SessionPromptParams
				json.Unmarshal(r.Params, &params) //nolint:errcheck

				// Signal that we are about to block on promptBlock.
				select {
				case s.promptReady <- struct{}{}:
				default:
				}

				// Block until unblocked or timed out to prevent goroutine leak.
				select {
				case <-s.promptBlock:
				case <-time.After(30 * time.Second):
					return
				}

				result := acp.SessionPromptResult{
					SessionID: params.SessionID,
					Content:   "slow mock response",
				}
				raw, _ := json.Marshal(result)
				writeJSON(acp.Response{JSONRPC: "2.0", ID: r.ID, Result: raw})

			case "session/cancel":
				var params acp.SessionCancelParams
				json.Unmarshal(r.Params, &params) //nolint:errcheck

				s.mu.Lock()
				s.cancelCalls = append(s.cancelCalls, params.SessionID)
				s.mu.Unlock()

				result := map[string]interface{}{"sessionId": params.SessionID, "canceled": true}
				raw, _ := json.Marshal(result)
				writeJSON(acp.Response{JSONRPC: "2.0", ID: r.ID, Result: raw})
			}
		}()
	}
}

// buildTestMessage returns a marshalled SendMessageRequest for use in shutdown tests.
func buildTestMessage(taskID string) []byte {
	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: taskID + "-msg",
			TaskID:    taskID,
			Role:      "user",
			Parts:     []a2a.Part{{Text: "test prompt"}},
		},
	}
	data, _ := json.Marshal(req)
	return data
}

// TestShutdownNoInflight verifies that Shutdown returns immediately (well within
// the grace period) when there are no in-flight Handle calls.
func TestShutdownNoInflight(t *testing.T) {
	handler := &Handler{
		logger:   nil,
		sessions: make(map[string]struct{}),
	}
	handler.logger = noopLogger()

	sm := NewShutdownManager(handler, 5*time.Second, noopLogger())

	start := time.Now()
	if err := sm.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("expected fast shutdown with no in-flight handles, took %v", elapsed)
	}
}

// TestShutdownInflightCompletesBeforeTimeout verifies that Shutdown waits for
// an in-flight Handle call that completes before the grace period expires.
func TestShutdownInflightCompletesBeforeTimeout(t *testing.T) {
	srv := startSlowACPServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	acpClient := acp.NewClient(srv.addr(), nil)
	if err := acpClient.Connect(ctx); err != nil {
		t.Fatalf("acp connect: %v", err)
	}
	defer acpClient.Close()

	handler := NewHandler(acpClient, nil, "/workspace", noopLogger())
	sm := NewShutdownManager(handler, 5*time.Second, noopLogger())

	msgData := buildTestMessage("task-inflight-ok")
	handleErr := make(chan error, 1)
	go func() {
		handleErr <- handler.Handle(sm.WorkContext(), msgData)
	}()

	// Wait until the slow server is blocking on promptBlock.
	select {
	case <-srv.promptReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ACP server to start blocking")
	}

	// Unblock the server so Handle can complete, then start shutdown.
	close(srv.promptBlock)

	if err := sm.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Handle should also have completed without error.
	select {
	case err := <-handleErr:
		if err != nil {
			t.Errorf("Handle returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Handle did not complete after shutdown")
	}
}

// TestShutdownInflightExceedsGracePeriod verifies that Shutdown waits for the
// full grace period when a Handle call is stuck, then forces completion via
// context cancellation.
func TestShutdownInflightExceedsGracePeriod(t *testing.T) {
	srv := startSlowACPServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	acpClient := acp.NewClient(srv.addr(), nil)
	if err := acpClient.Connect(ctx); err != nil {
		t.Fatalf("acp connect: %v", err)
	}
	defer acpClient.Close()

	gracePeriod := 150 * time.Millisecond
	handler := NewHandler(acpClient, nil, "/workspace", noopLogger())
	sm := NewShutdownManager(handler, gracePeriod, noopLogger())

	msgData := buildTestMessage("task-inflight-stuck")
	go func() {
		handler.Handle(sm.WorkContext(), msgData) //nolint:errcheck
	}()

	// Wait until ACP server is blocking on prompt.
	select {
	case <-srv.promptReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ACP server to start blocking")
	}

	start := time.Now()
	// promptBlock is NOT closed; Shutdown must time out and force-cancel.
	sm.Shutdown(context.Background()) //nolint:errcheck
	elapsed := time.Since(start)

	// Shutdown must have waited at least the grace period.
	if elapsed < gracePeriod {
		t.Errorf("shutdown completed too fast (%v < grace period %v): did not wait for in-flight handle",
			elapsed, gracePeriod)
	}
}

// TestShutdownCancelsACPSession verifies that Shutdown sends session/cancel to
// the ACP agent when the grace period expires with an active session.
func TestShutdownCancelsACPSession(t *testing.T) {
	srv := startSlowACPServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	acpClient := acp.NewClient(srv.addr(), nil)
	if err := acpClient.Connect(ctx); err != nil {
		t.Fatalf("acp connect: %v", err)
	}
	defer acpClient.Close()

	gracePeriod := 150 * time.Millisecond
	handler := NewHandler(acpClient, nil, "/workspace", noopLogger())
	sm := NewShutdownManager(handler, gracePeriod, noopLogger())

	msgData := buildTestMessage("task-cancel-check")
	go func() {
		handler.Handle(sm.WorkContext(), msgData) //nolint:errcheck
	}()

	// Wait until ACP server is blocking on the prompt.
	select {
	case <-srv.promptReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for ACP server to start blocking")
	}

	// Trigger shutdown; grace period is short so it will expire.
	sm.Shutdown(context.Background()) //nolint:errcheck

	// The mock server should have received session/cancel.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.cancelCallCount() > 0 {
			return // success
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("expected ACP session/cancel to be sent, but cancelCalls=%d", srv.cancelCallCount())
}

// noopLogger returns a logger that discards all output (keeps test output clean).
func noopLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(noopWriter{}, nil))
}

type noopWriter struct{}

func (noopWriter) Write(p []byte) (int, error) { return len(p), nil }
