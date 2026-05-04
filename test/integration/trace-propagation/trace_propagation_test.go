//go:build integration

// Package tracepropagation runs the Phase 6 sub-task 6.1 integration test
// that asserts W3C TraceContext propagates across every hop of the daedalus
// task pipeline, under 100-task concurrent load.
//
// See README.md in this directory for the full audit, the expected span
// tree, and the run command.
package tracepropagation

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/acp"
	"github.com/raykao/daedalus/internal/orchestrator"
	"github.com/raykao/daedalus/internal/proxy"
	"github.com/raykao/daedalus/internal/queue"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// ---------------------------------------------------------------------------
// Embedded NATS with the streams the proxy + collector need.
// ---------------------------------------------------------------------------

func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	srv := natsserver.RunServer(&opts)
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, cfg := range []jetstream.StreamConfig{
		{Name: "AGENT_TASKS", Subjects: []string{"agent.tasks.>"}},
		{Name: "AGENT_RESULTS", Subjects: []string{"agent.results.>"}},
		{Name: "AGENT_STATUS", Subjects: []string{"agent.status.>"}},
	} {
		if _, err := js.CreateStream(ctx, cfg); err != nil {
			t.Fatalf("create stream %s: %v", cfg.Name, err)
		}
	}

	return srv.ClientURL()
}

// ---------------------------------------------------------------------------
// Fake ACP server: minimal initialize / session/new / session/prompt loop.
// Concurrency-safe: every connection gets its own goroutine and session map.
// ---------------------------------------------------------------------------

func startFakeACPServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake acp listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeACP(c)
		}
	}()
	return ln
}

func serveFakeACP(c net.Conn) {
	defer c.Close()
	scanner := bufio.NewScanner(c)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var sessionCounter atomic.Int64

	writeJSON := func(v any) {
		data, _ := json.Marshal(v)
		data = append(data, '\n')
		_, _ = c.Write(data)
	}

	for scanner.Scan() {
		var req acp.Request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			result := acp.InitializeResult{
				ProtocolVersion: 1,
				Capabilities:    acp.ServerCapabilities{Streaming: true},
				ServerInfo:      acp.ServerInfo{Name: "fake", Version: "0.0.1"},
			}
			raw, _ := json.Marshal(result)
			writeJSON(acp.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
		case "session/new":
			id := sessionCounter.Add(1)
			result := acp.SessionNewResult{SessionID: fmt.Sprintf("sess-%d", id)}
			raw, _ := json.Marshal(result)
			writeJSON(acp.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
		case "session/prompt":
			var params acp.SessionPromptParams
			_ = json.Unmarshal(req.Params, &params)
			text := ""
			if len(params.Prompt) > 0 {
				text = params.Prompt[0].Text
			}
			result := acp.SessionPromptResult{
				SessionID: params.SessionID,
				Content:   "ack:" + text,
			}
			raw, _ := json.Marshal(result)
			writeJSON(acp.Response{JSONRPC: "2.0", ID: req.ID, Result: raw})
		case "session/cancel":
			writeJSON(acp.Response{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{}`)})
		default:
			// Unknown method - silently ignore
		}
	}
}

// ---------------------------------------------------------------------------
// In-memory trace recorder.
//
// Choice rationale (also documented in README.md): we use
// tracetest.InMemoryExporter with a SimpleSpanProcessor so that
// tp.ForceFlush() guarantees all spans are visible to assertions
// synchronously. A real OTLP receiver would test wire format, not
// propagation correctness, and is out of scope.
// ---------------------------------------------------------------------------

func newTracerProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.InMemoryExporter) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})
	return tp, exp
}

// ---------------------------------------------------------------------------
// The test.
// ---------------------------------------------------------------------------

const numTasks = 100

func TestTracePropagation_100ConcurrentTasks(t *testing.T) {
	tp, exp := newTracerProvider(t)

	natsURL := startEmbeddedNATS(t)
	acpLn := startFakeACPServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// ACP client connects to fake server.
	acpClient := acp.NewClient(acpLn.Addr().String(), nil)
	if err := acpClient.Connect(ctx); err != nil {
		t.Fatalf("acp connect: %v", err)
	}
	t.Cleanup(func() { _ = acpClient.Close() })
	if _, err := acpClient.Initialize(ctx); err != nil {
		t.Fatalf("acp initialize: %v", err)
	}

	// NATS consumer (proxy side).
	consumer, err := queue.NewConsumer(ctx, queue.Config{
		URL:     natsURL,
		Stream:  "AGENT_TASKS",
		Subject: "agent.tasks.>",
	})
	if err != nil {
		t.Fatalf("queue.NewConsumer: %v", err)
	}
	t.Cleanup(consumer.Close)

	// Publisher reuses the consumer's connection.
	publisher, err := queue.NewPublisher(consumer.Conn(), nil)
	if err != nil {
		t.Fatalf("queue.NewPublisher: %v", err)
	}

	// Proxy handler. SetInitialized so Handle does not call Initialize again.
	handler := proxy.NewHandler(acpClient, publisher, t.TempDir(), nil, nil)
	handler.SetInitialized()

	consumerCtx, consumerCancel := context.WithCancel(ctx)
	defer consumerCancel()
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		_ = consumer.Run(consumerCtx, handler.Handle)
	}()

	// Orchestrator collector on its own NATS connection, since core-NATS
	// subscribes are most natural here.
	collectorNC, err := nats.Connect(natsURL)
	if err != nil {
		t.Fatalf("collector nats connect: %v", err)
	}
	t.Cleanup(collectorNC.Close)
	collector := orchestrator.NewResultCollector(collectorNC, nil)
	collectorCtx, collectorCancel := context.WithCancel(ctx)
	defer collectorCancel()
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		_ = collector.Start(collectorCtx)
	}()

	// Allow subscriptions to register before publishing tasks.
	time.Sleep(100 * time.Millisecond)

	// ----------------------------------------------------------------------
	// Publish 100 tasks concurrently. Each task gets its own root span
	// "test.dispatch", and the publisher injects the trace context into
	// the NATS headers via the now-instrumented Publisher.PublishJSON.
	// ----------------------------------------------------------------------

	tracer := tp.Tracer("test.trace-propagation")
	taskIDs := make([]string, numTasks)
	rootTraceIDs := make([]oteltrace.TraceID, numTasks)

	var wg sync.WaitGroup
	wg.Add(numTasks)
	for i := 0; i < numTasks; i++ {
		i := i
		go func() {
			defer wg.Done()
			taskID := fmt.Sprintf("task-%03d", i)
			taskIDs[i] = taskID

			rootCtx, rootSpan := tracer.Start(ctx, "test.dispatch")
			rootTraceIDs[i] = rootSpan.SpanContext().TraceID()

			req := a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: taskID,
					TaskID:    taskID,
					Role:      "user",
					Parts:     []a2a.Part{{Text: fmt.Sprintf("hello %d", i)}},
				},
			}
			subject := "agent.tasks." + taskID
			if err := publisher.PublishJSON(rootCtx, subject, req); err != nil {
				t.Errorf("publish %s: %v", taskID, err)
			}
			rootSpan.End()
		}()
	}
	wg.Wait()

	// ----------------------------------------------------------------------
	// Wait for every task's terminal result via the collector.
	// ----------------------------------------------------------------------
	results, err := collector.WaitForAll(ctx, taskIDs)
	if err != nil {
		t.Fatalf("WaitForAll: %v (got %d/%d results)", err, len(results), numTasks)
	}
	if got := len(results); got != numTasks {
		t.Fatalf("expected %d results, got %d", numTasks, got)
	}
	for id, r := range results {
		if r.Status != a2a.TaskStateCompleted {
			t.Fatalf("task %s did not complete (state=%s)", id, r.Status)
		}
	}

	// Stop pipeline and let in-flight spans finish.
	consumerCancel()
	collectorCancel()
	<-consumerDone
	<-collectorDone

	if err := tp.ForceFlush(ctx); err != nil {
		t.Fatalf("ForceFlush: %v", err)
	}

	// ----------------------------------------------------------------------
	// Assertions.
	// ----------------------------------------------------------------------
	spans := exp.GetSpans()
	t.Logf("captured %d spans across %d tasks", len(spans), numTasks)

	assertTraceTrees(t, spans, taskIDs, rootTraceIDs)
}

// ---------------------------------------------------------------------------
// Span graph assertions.
// ---------------------------------------------------------------------------

// expectedSpansPerTask is the canonical span-name set documented in README.md.
// The proxy emits "proxy.handle" with no taskId suffix in the span name; the
// task ID is on an attribute. Likewise "acp.session.new" / "acp.session.prompt"
// are global names. The publish/consume span names embed the subject, so we
// match per-subject below.
var expectedSpanNamesPerTask = []string{
	"test.dispatch",
	"proxy.handle",
	"acp.session.new",
	"acp.session.prompt",
	"collector.receive.result",
}

// expectedDynamicSpansPerTask returns the span names that include the taskID
// (queue publish/consume on agent.tasks.* and agent.results.*).
func expectedDynamicSpansPerTask(taskID string) []string {
	return []string{
		"nats.publish agent.tasks." + taskID,
		"nats.consume agent.tasks." + taskID,
		"nats.publish agent.results." + taskID,
	}
}

func assertTraceTrees(t *testing.T, spans tracetest.SpanStubs, taskIDs []string, rootTraceIDs []oteltrace.TraceID) {
	t.Helper()

	// Group spans by trace ID for fast lookup.
	byTrace := map[oteltrace.TraceID][]tracetest.SpanStub{}
	bySpanID := map[oteltrace.SpanID]tracetest.SpanStub{}
	for _, s := range spans {
		byTrace[s.SpanContext.TraceID()] = append(byTrace[s.SpanContext.TraceID()], s)
		bySpanID[s.SpanContext.SpanID()] = s
	}

	// Aggregate assertion 1: distinct trace IDs == numTasks (no collisions).
	if got := len(byTrace); got != numTasks {
		t.Errorf("aggregate: expected %d distinct trace IDs, got %d", numTasks, got)
	}

	// Aggregate assertion 2: total root spans == numTasks.
	totalRoots := 0
	for _, s := range spans {
		if !s.Parent.IsValid() {
			totalRoots++
		}
	}
	if totalRoots != numTasks {
		t.Errorf("aggregate: expected %d roots, got %d", numTasks, totalRoots)
	}

	// Per-task assertions.
	for i, taskID := range taskIDs {
		traceID := rootTraceIDs[i]
		traceSpans := byTrace[traceID]
		if len(traceSpans) == 0 {
			t.Errorf("task %s (trace %s): no spans captured", taskID, traceID)
			continue
		}

		// (a) Exactly one root.
		var rootCount int
		var rootName string
		for _, s := range traceSpans {
			if !s.Parent.IsValid() {
				rootCount++
				rootName = s.Name
			}
		}
		if rootCount != 1 {
			t.Errorf("task %s (trace %s): expected 1 root, got %d", taskID, traceID, rootCount)
		}
		if rootName != "test.dispatch" {
			t.Errorf("task %s (trace %s): root name = %q, want test.dispatch", taskID, traceID, rootName)
		}

		// (b) Expected child spans by name.
		names := map[string]int{}
		for _, s := range traceSpans {
			names[s.Name]++
		}
		for _, want := range expectedSpanNamesPerTask {
			if names[want] == 0 {
				t.Errorf("task %s (trace %s): missing expected span %q (have: %v)", taskID, traceID, want, sortedNames(names))
			}
		}
		for _, want := range expectedDynamicSpansPerTask(taskID) {
			if names[want] == 0 {
				t.Errorf("task %s (trace %s): missing expected span %q (have: %v)", taskID, traceID, want, sortedNames(names))
			}
		}

		// (c) No orphan: every non-root span's parent must exist somewhere
		//     (in this trace, since trace ID is consistent - see (d)).
		for _, s := range traceSpans {
			if !s.Parent.IsValid() {
				continue
			}
			parent, ok := bySpanID[s.Parent.SpanID()]
			if !ok {
				t.Errorf("task %s (trace %s): span %q has unknown parent span_id %s",
					taskID, traceID, s.Name, s.Parent.SpanID())
				continue
			}
			if parent.SpanContext.TraceID() != s.SpanContext.TraceID() {
				t.Errorf("task %s: span %q is in trace %s but parent %q is in trace %s (cross-trace orphan)",
					taskID, s.Name, s.SpanContext.TraceID(), parent.Name, parent.SpanContext.TraceID())
			}
		}

		// (d) Every span in the group shares the same trace_id.
		for _, s := range traceSpans {
			if s.SpanContext.TraceID() != traceID {
				t.Errorf("task %s: span %q has trace %s, expected %s",
					taskID, s.Name, s.SpanContext.TraceID(), traceID)
			}
		}
	}
}

func sortedNames(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
