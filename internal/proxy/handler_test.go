package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/acp"
	contextmgmt "github.com/raykao/daedalus/internal/contextmgmt"
	"github.com/raykao/daedalus/internal/queue"
)

// startMockACPServer starts a mock ACP server that handles the full initialize/new-session/prompt flow
func startMockACPServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mock ACP server listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleMockACPConn(conn)
		}
	}()
	return ln
}

func handleMockACPConn(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, len(buf))
	sessionCounter := 0

	writeJSON := func(v interface{}) {
		data, _ := json.Marshal(v)
		data = append(data, '\n')
		conn.Write(data)
	}

	for scanner.Scan() {
		var req acp.Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			result := acp.InitializeResult{
				ProtocolVersion: "2025-01-01",
				Capabilities:    acp.ServerCapabilities{Streaming: true},
				ServerInfo:      acp.ServerInfo{Name: "mock-agent", Version: "1.0.0"},
			}
			raw, _ := json.Marshal(result)
			writeJSON(acp.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})

		case "session/new":
			sessionCounter++
			result := acp.SessionNewResult{SessionID: fmt.Sprintf("sess-%d", sessionCounter)}
			raw, _ := json.Marshal(result)
			writeJSON(acp.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})

		case "session/prompt":
			var params acp.SessionPromptParams
			json.Unmarshal(req.Params, &params)

			// Send a delta notification
			deltaParams := acp.MessageDeltaParams{SessionID: params.SessionID, Content: "mock "}
			raw, _ := json.Marshal(deltaParams)
			writeJSON(acp.Response{JSONRPC: "2.0", Method: "assistant.message_delta", Params: raw})

			time.Sleep(5 * time.Millisecond)

			// Final result
			result := acp.SessionPromptResult{
				SessionID: params.SessionID,
				Content:   "mock response to: " + params.Prompt,
			}
			raw, _ = json.Marshal(result)
			writeJSON(acp.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
		}
	}
}

func setupNATSWithStreams(t *testing.T) (string, func()) {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		t.Fatalf("nats connect: %v", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		srv.Shutdown()
		t.Fatalf("jetstream: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create streams for results and status
	for _, cfg := range []jetstream.StreamConfig{
		{Name: "RESULTS", Subjects: []string{"agent.results.>"}},
		{Name: "STATUS", Subjects: []string{"agent.status.>"}},
	} {
		if _, err := js.CreateStream(ctx, cfg); err != nil {
			nc.Close()
			srv.Shutdown()
			t.Fatalf("create stream %s: %v", cfg.Name, err)
		}
	}
	nc.Close()

	return srv.ClientURL(), func() { srv.Shutdown() }
}

func TestHandlerFullFlow(t *testing.T) {
	// Start mock ACP server
	acpLn := startMockACPServer(t)
	defer acpLn.Close()

	// Start NATS
	natsURL, cleanup := setupNATSWithStreams(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Connect ACP client
	acpClient := acp.NewClient(acpLn.Addr().String(), nil)
	if err := acpClient.Connect(ctx); err != nil {
		t.Fatalf("acp connect: %v", err)
	}
	defer acpClient.Close()

	// Connect NATS publisher
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	publisher, err := queue.NewPublisher(nc, nil)
	if err != nil {
		t.Fatalf("publisher: %v", err)
	}

	handler := NewHandler(acpClient, publisher, "/workspace", nil, nil)

	// Build test request
	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "msg-001",
			TaskID:    "task-001",
			Role:      "user",
			Parts:     []a2a.Part{{Text: "write a hello world in Go"}},
		},
	}
	data, _ := json.Marshal(req)

	// Subscribe to results before handling
	js, _ := jetstream.New(nc)
	resultSub, err := js.CreateOrUpdateConsumer(ctx, "RESULTS", jetstream.ConsumerConfig{
		Name:          "test-result-consumer",
		Durable:       "test-result-consumer",
		FilterSubject: "agent.results.task-001",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("create result consumer: %v", err)
	}

	// Handle the message
	if err := handler.Handle(ctx, data); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Check that result was published
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		msgs, err := resultSub.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
		if err != nil {
			t.Errorf("fetch result: %v", err)
			return
		}
		for msg := range msgs.Messages() {
			var task a2a.Task
			if err := json.Unmarshal(msg.Data(), &task); err != nil {
				t.Errorf("unmarshal task: %v", err)
				return
			}
			if task.ID != "task-001" {
				t.Errorf("expected task ID task-001, got %s", task.ID)
			}
			if task.Status.State != a2a.TaskStateCompleted {
				t.Errorf("expected completed state, got %s", task.Status.State)
			}
			msg.Ack()
		}
	}()
	wg.Wait()
}

func TestHandlerEmptyPrompt(t *testing.T) {
	acpLn := startMockACPServer(t)
	defer acpLn.Close()

	natsURL, cleanup := setupNATSWithStreams(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	acpClient := acp.NewClient(acpLn.Addr().String(), nil)
	if err := acpClient.Connect(ctx); err != nil {
		t.Fatalf("acp connect: %v", err)
	}
	defer acpClient.Close()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	publisher, err := queue.NewPublisher(nc, nil)
	if err != nil {
		t.Fatalf("publisher: %v", err)
	}

	handler := NewHandler(acpClient, publisher, "/workspace", nil, nil)

	// Message with no text parts
	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "msg-empty",
			TaskID:    "task-empty",
			Role:      "user",
			Parts:     []a2a.Part{},
		},
	}
	data, _ := json.Marshal(req)

	err = handler.Handle(ctx, data)
	if err == nil {
		t.Fatal("expected error for empty prompt, got nil")
	}
}

func TestHandlerContextTracking(t *testing.T) {
	// Start mock ACP server
	acpLn := startMockACPServer(t)
	defer acpLn.Close()

	// Start NATS
	natsURL, cleanup := setupNATSWithStreams(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Connect ACP client
	acpClient := acp.NewClient(acpLn.Addr().String(), nil)
	if err := acpClient.Connect(ctx); err != nil {
		t.Fatalf("acp connect: %v", err)
	}
	defer acpClient.Close()

	// Connect NATS publisher
	nc, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	publisher, err := queue.NewPublisher(nc, nil)
	if err != nil {
		t.Fatalf("publisher: %v", err)
	}

	// Create tracker with known config
	tracker, err := contextmgmt.NewTracker(contextmgmt.DefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(acpClient, publisher, "/workspace", nil, tracker)

	// Build test request
	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "msg-ctx",
			TaskID:    "task-ctx",
			Role:      "user",
			Parts:     []a2a.Part{{Text: "test context tracking"}},
		},
	}
	data, _ := json.Marshal(req)

	// Subscribe to results before handling
	js, _ := jetstream.New(nc)
	resultSub, err := js.CreateOrUpdateConsumer(ctx, "RESULTS", jetstream.ConsumerConfig{
		Name:          "test-ctx-consumer",
		Durable:       "test-ctx-consumer",
		FilterSubject: "agent.results.task-ctx",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("create result consumer: %v", err)
	}

	// Handle the message
	if err := handler.Handle(ctx, data); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	// Check that result was published with context metadata
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		msgs, err := resultSub.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
		if err != nil {
			t.Errorf("fetch result: %v", err)
			return
		}
		for msg := range msgs.Messages() {
			var task a2a.Task
			if err := json.Unmarshal(msg.Data(), &task); err != nil {
				t.Errorf("unmarshal task: %v", err)
				return
			}
			if task.ID != "task-ctx" {
				t.Errorf("expected task ID task-ctx, got %s", task.ID)
			}
			// Verify context usage metadata is present
			if task.Metadata == nil {
				t.Error("expected task metadata to be set")
				msg.Ack()
				return
			}
			ctxUsage, ok := task.Metadata["contextUsage"]
			if !ok {
				t.Error("expected contextUsage in task metadata")
				msg.Ack()
				return
			}
			usage, ok := ctxUsage.(map[string]interface{})
			if !ok {
				t.Errorf("expected contextUsage to be map, got %T", ctxUsage)
				msg.Ack()
				return
			}
			// turnCount should be 1 (one prompt/response cycle)
			if tc, ok := usage["turnCount"]; ok {
				// JSON numbers unmarshal as float64
				if tcf, ok := tc.(float64); ok && tcf != 1 {
					t.Errorf("expected turnCount 1, got %v", tc)
				}
			} else {
				t.Error("expected turnCount in contextUsage")
			}
			// currentTokens should be > 0 (approximated from content length)
			if ct, ok := usage["currentTokens"]; ok {
				if ctf, ok := ct.(float64); ok && ctf <= 0 {
					t.Errorf("expected currentTokens > 0, got %v", ct)
				}
			} else {
				t.Error("expected currentTokens in contextUsage")
			}
			msg.Ack()
		}
	}()
	wg.Wait()

	// After Handle completes, session should be cleaned up (ended by defer)
	if tracker.ActiveSessionCount() != 0 {
		t.Errorf("expected 0 active sessions after Handle, got %d", tracker.ActiveSessionCount())
	}
}
