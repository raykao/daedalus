package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

// mockACPServer is a test helper that simulates an ACP agent over TCP
type mockACPServer struct {
	listener net.Listener
	t        *testing.T
}

func startMockServer(t *testing.T, handler func(conn net.Conn)) *mockACPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	srv := &mockACPServer{listener: ln, t: t}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go handler(conn)
		}
	}()
	return srv
}

func (s *mockACPServer) Addr() string {
	return s.listener.Addr().String()
}

func (s *mockACPServer) Close() {
	s.listener.Close()
}

// writeJSON writes a JSON-RPC message as NDJSON to conn
func writeJSON(conn net.Conn, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = conn.Write(data)
	return err
}

// readRequest reads one JSON-RPC request from conn
func readRequest(conn net.Conn) (*Request, error) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return nil, scanner.Err()
	}
	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		return nil, err
	}
	return &req, nil
}

func TestInitialize(t *testing.T) {
	srv := startMockServer(t, func(conn net.Conn) {
		defer conn.Close()
		req, err := readRequest(conn)
		if err != nil || req.Method != "initialize" {
			t.Errorf("expected initialize, got %v (err: %v)", req, err)
			return
		}
		result := InitializeResult{
			ProtocolVersion: 1,
			Capabilities:    ServerCapabilities{Streaming: true, LoadSession: true},
			ServerInfo:      ServerInfo{Name: "test-agent", Version: "1.0.0"},
		}
		raw, _ := json.Marshal(result)
		writeJSON(conn, Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
	})
	defer srv.Close()

	client := NewClient(srv.Addr(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	result, err := client.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.ProtocolVersion != 1 {
		t.Errorf("expected protocolVersion 1, got %d", result.ProtocolVersion)
	}
}

func TestNewSession(t *testing.T) {
	srv := startMockServer(t, func(conn net.Conn) {
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var req Request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			switch req.Method {
			case "session/new":
				result := SessionNewResult{SessionID: "test-session-123"}
				raw, _ := json.Marshal(result)
				writeJSON(conn, Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
			}
		}
	})
	defer srv.Close()

	client := NewClient(srv.Addr(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	sessionID, err := client.NewSession(ctx, "/workspace")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if sessionID != "test-session-123" {
		t.Errorf("expected session ID test-session-123, got %s", sessionID)
	}
}

func TestPromptWithStreamingDeltas(t *testing.T) {
	const expectedContent = "Hello, world!"

	srv := startMockServer(t, func(conn net.Conn) {
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var req Request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if req.Method != "session/prompt" {
				continue
			}

			var params SessionPromptParams
			json.Unmarshal(req.Params, &params)

			// Send streaming deltas as session/update agent_message_chunk notifications
			for _, chunk := range []string{"Hello", ", ", "world", "!"} {
				updateParams := SessionUpdateParams{
					SessionID: params.SessionID,
					Update: SessionUpdateBody{
						SessionUpdate: "agent_message_chunk",
						Content:       SessionUpdateContent{Type: "text", Text: chunk},
					},
				}
				raw, _ := json.Marshal(updateParams)
				writeJSON(conn, Response{
					JSONRPC: "2.0",
					Method:  "session/update",
					Params:  raw,
				})
				time.Sleep(5 * time.Millisecond)
			}

			// Final response
			result := SessionPromptResult{
				SessionID: params.SessionID,
				Content:   expectedContent,
			}
			raw, _ := json.Marshal(result)
			writeJSON(conn, Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
		}
	})
	defer srv.Close()

	client := NewClient(srv.Addr(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	content, err := client.Prompt(ctx, "test-session", "implement X")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if content != expectedContent {
		t.Errorf("expected %q, got %q", expectedContent, content)
	}
}

func TestPromptRPCError(t *testing.T) {
	srv := startMockServer(t, func(conn net.Conn) {
		defer conn.Close()
		req, _ := readRequest(conn)
		if req != nil && req.Method == "session/prompt" {
			writeJSON(conn, Response{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &RPCError{Code: -32001, Message: "session not found"},
			})
		}
	})
	defer srv.Close()

	client := NewClient(srv.Addr(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	_, err := client.Prompt(ctx, "bad-session", "test")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// TestPermissionRequestIsServerRequest verifies that session/request_permission with
// a non-nil ID is routed to handleServerRequest and answered with a proper JSON-RPC
// response (not a notification), unblocking the CLI.
func TestPermissionRequestIsServerRequest(t *testing.T) {
	var permissionResponseReceived = make(chan map[string]interface{}, 1)

	srv := startMockServer(t, func(conn net.Conn) {
		defer conn.Close()
		scanner := bufio.NewScanner(conn)

		// Step 1: expect initialize or session/prompt (ignore them, just drive the test)
		// We send a permission request mid-stream as the CLI does.
		for scanner.Scan() {
			var req Request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if req.Method == "session/prompt" {
				// Send a server-to-client request with id=0 (as CLI v1.0.36 does)
				permID := int64(0)
				permParams := PermissionRequestParams{
					SessionID: "test-perm-session",
				}
				raw, _ := json.Marshal(permParams)
				writeJSON(conn, Response{
					JSONRPC: "2.0",
					ID:      &permID,
					Method:  "session/request_permission",
					Params:  raw,
				})

				// Read the response to our permission request
				if scanner.Scan() {
					var resp map[string]interface{}
					if err := json.Unmarshal(scanner.Bytes(), &resp); err == nil {
						permissionResponseReceived <- resp
					}
				}

				// Now send the final prompt response
				result := SessionPromptResult{SessionID: "test-perm-session", Content: "done"}
				resultRaw, _ := json.Marshal(result)
				writeJSON(conn, Response{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
				return
			}
		}
	})
	defer srv.Close()

	client := NewClient(srv.Addr(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	// Run Prompt in a goroutine; it will block until permission is handled
	promptDone := make(chan error, 1)
	go func() {
		_, err := client.Prompt(ctx, "test-perm-session", "do something requiring permission")
		promptDone <- err
	}()

	// Verify the permission response was a proper JSON-RPC response (not a notification)
	select {
	case <-ctx.Done():
		t.Fatal("timed out waiting for permission response")
	case resp := <-permissionResponseReceived:
		if resp["jsonrpc"] != "2.0" {
			t.Errorf("expected jsonrpc 2.0, got %v", resp["jsonrpc"])
		}
		if resp["id"] == nil {
			t.Error("permission response must have an id (it's a response, not a notification)")
		}
		result, ok := resp["result"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected result object, got %T: %v", resp["result"], resp["result"])
		}
		outcome, ok := result["outcome"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected outcome object, got %T: %v", result["outcome"], result["outcome"])
		}
		if outcome["optionId"] != "allow_once" {
			t.Errorf("expected optionId allow_once, got %v", outcome["optionId"])
		}
	}

	// Prompt should complete successfully
	select {
	case <-ctx.Done():
		t.Fatal("timed out waiting for Prompt to complete")
	case err := <-promptDone:
		if err != nil {
			t.Fatalf("Prompt returned error: %v", err)
		}
	}
}

// TestAgentThoughtChunkIgnored verifies that agent_thought_chunk session/update
// notifications do NOT add content to the delta channel (only agent_message_chunk does).
func TestAgentThoughtChunkIgnored(t *testing.T) {
	srv := startMockServer(t, func(conn net.Conn) {
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			var req Request
			if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
				continue
			}
			if req.Method != "session/prompt" {
				continue
			}
			var params SessionPromptParams
			json.Unmarshal(req.Params, &params)

			// thought_chunk should be ignored
			thoughtParams := SessionUpdateParams{
				SessionID: params.SessionID,
				Update: SessionUpdateBody{
					SessionUpdate: "agent_thought_chunk",
					Content:       SessionUpdateContent{Type: "text", Text: "internal thinking"},
				},
			}
			raw, _ := json.Marshal(thoughtParams)
			writeJSON(conn, Response{JSONRPC: "2.0", Method: "session/update", Params: raw})
			time.Sleep(5 * time.Millisecond)

			// message_chunk should be captured
			msgParams := SessionUpdateParams{
				SessionID: params.SessionID,
				Update: SessionUpdateBody{
					SessionUpdate: "agent_message_chunk",
					Content:       SessionUpdateContent{Type: "text", Text: "pong"},
				},
			}
			raw2, _ := json.Marshal(msgParams)
			writeJSON(conn, Response{JSONRPC: "2.0", Method: "session/update", Params: raw2})
			time.Sleep(5 * time.Millisecond)

			// Final response with no content - forces fallback to deltas
			result := SessionPromptResult{SessionID: params.SessionID, Content: ""}
			resultRaw, _ := json.Marshal(result)
			writeJSON(conn, Response{JSONRPC: "2.0", ID: req.ID, Result: resultRaw})
		}
	})
	defer srv.Close()

	client := NewClient(srv.Addr(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	content, err := client.Prompt(ctx, "thought-test-session", "ping")
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// Should only contain the message_chunk, not the thought_chunk
	if strings.Contains(content, "internal thinking") {
		t.Errorf("thought chunk leaked into content: %q", content)
	}
	if content != "pong" {
		t.Errorf("expected %q, got %q", "pong", content)
	}
}
