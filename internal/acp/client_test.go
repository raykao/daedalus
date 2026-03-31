package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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
			ProtocolVersion: "2025-01-01",
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
	if result.ProtocolVersion != "2025-01-01" {
		t.Errorf("expected protocolVersion 2025-01-01, got %s", result.ProtocolVersion)
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

			// Send streaming deltas
			for _, chunk := range []string{"Hello", ", ", "world", "!"} {
				deltaParams := MessageDeltaParams{SessionID: params.SessionID, Content: chunk}
				raw, _ := json.Marshal(deltaParams)
				writeJSON(conn, Response{
					JSONRPC: "2.0",
					Method:  "assistant.message_delta",
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
