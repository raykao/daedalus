package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/orchestrator"
)

// startEmbeddedNATS starts an in-process NATS server (no JetStream needed for
// core pub/sub) and returns the client URL. The server is shut down when the
// test finishes.
func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1 // random available port
	srv := natsserver.RunServer(&opts)
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

func connectNATS(t *testing.T, url string) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("nats.Connect(%s): %v", url, err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// publishTask publishes an a2a.Task to agent.results.<taskID>.
func publishTask(t *testing.T, nc *nats.Conn, taskID string, state a2a.TaskState) {
	t.Helper()
	task := a2a.Task{
		ID:        taskID,
		ContextID: "ctx-test",
		Status:    a2a.TaskStatus{State: state},
	}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal task: %v", err)
	}
	if err := nc.Publish("agent.results."+taskID, data); err != nil {
		t.Fatalf("publish result for %s: %v", taskID, err)
	}
}

// publishStatus publishes an a2a.TaskStatus to agent.status.<taskID>.
func publishStatus(t *testing.T, nc *nats.Conn, taskID string, state a2a.TaskState) {
	t.Helper()
	status := a2a.TaskStatus{State: state}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	if err := nc.Publish("agent.status."+taskID, data); err != nil {
		t.Fatalf("publish status for %s: %v", taskID, err)
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestResultCollector_WaitForReturnsResult(t *testing.T) {
	url := startEmbeddedNATS(t)
	nc := connectNATS(t, url)

	rc := orchestrator.NewResultCollector(nc, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start collector in background.
	startedCh := make(chan struct{})
	go func() {
		close(startedCh)
		rc.Start(ctx) //nolint:errcheck
	}()
	<-startedCh
	// Give the subscriptions a moment to be active.
	time.Sleep(20 * time.Millisecond)

	// Publish a result asynchronously.
	go func() {
		time.Sleep(30 * time.Millisecond)
		publishTask(t, nc, "task-001", a2a.TaskStateCompleted)
	}()

	result, err := rc.WaitFor(ctx, "task-001")
	if err != nil {
		t.Fatalf("WaitFor returned error: %v", err)
	}
	if result == nil {
		t.Fatal("WaitFor returned nil result")
	}
	if result.TaskID != "task-001" {
		t.Errorf("expected taskID %q, got %q", "task-001", result.TaskID)
	}
	if result.Status != a2a.TaskStateCompleted {
		t.Errorf("expected status %q, got %q", a2a.TaskStateCompleted, result.Status)
	}
}

func TestResultCollector_WaitForTimesOutOnContextCancellation(t *testing.T) {
	url := startEmbeddedNATS(t)
	nc := connectNATS(t, url)

	rc := orchestrator.NewResultCollector(nc, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	startedCh := make(chan struct{})
	go func() {
		close(startedCh)
		rc.Start(ctx) //nolint:errcheck
	}()
	<-startedCh
	time.Sleep(20 * time.Millisecond)

	// Use a very short timeout so the wait expires.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer waitCancel()

	_, err := rc.WaitFor(waitCtx, "task-never-published")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestResultCollector_WaitForAllCollectsAllResults(t *testing.T) {
	url := startEmbeddedNATS(t)
	nc := connectNATS(t, url)

	rc := orchestrator.NewResultCollector(nc, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startedCh := make(chan struct{})
	go func() {
		close(startedCh)
		rc.Start(ctx) //nolint:errcheck
	}()
	<-startedCh
	time.Sleep(20 * time.Millisecond)

	taskIDs := []string{"task-a", "task-b", "task-c"}

	// Publish all results with small delays.
	go func() {
		for i, id := range taskIDs {
			time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
			publishTask(t, nc, id, a2a.TaskStateCompleted)
		}
	}()

	results, err := rc.WaitForAll(ctx, taskIDs)
	if err != nil {
		t.Fatalf("WaitForAll returned error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for _, id := range taskIDs {
		if _, ok := results[id]; !ok {
			t.Errorf("missing result for taskID %q", id)
		}
	}
}

func TestResultCollector_ConcurrentWaitForSameTaskID(t *testing.T) {
	url := startEmbeddedNATS(t)
	nc := connectNATS(t, url)

	rc := orchestrator.NewResultCollector(nc, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startedCh := make(chan struct{})
	go func() {
		close(startedCh)
		rc.Start(ctx) //nolint:errcheck
	}()
	<-startedCh
	time.Sleep(20 * time.Millisecond)

	const taskID = "task-shared"
	const numWaiters = 5

	type outcome struct {
		result *orchestrator.TaskResult
		err    error
	}
	outcomes := make(chan outcome, numWaiters)

	for range numWaiters {
		go func() {
			r, e := rc.WaitFor(ctx, taskID)
			outcomes <- outcome{result: r, err: e}
		}()
	}

	// Give goroutines time to register their waiters.
	time.Sleep(30 * time.Millisecond)

	publishTask(t, nc, taskID, a2a.TaskStateCompleted)

	for range numWaiters {
		o := <-outcomes
		if o.err != nil {
			t.Errorf("WaitFor goroutine returned error: %v", o.err)
		}
		if o.result == nil || o.result.TaskID != taskID {
			t.Errorf("unexpected result: %+v", o.result)
		}
	}
}

func TestResultCollector_StatusUpdateForTerminalStateUnblocksWaiter(t *testing.T) {
	url := startEmbeddedNATS(t)
	nc := connectNATS(t, url)

	rc := orchestrator.NewResultCollector(nc, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	startedCh := make(chan struct{})
	go func() {
		close(startedCh)
		rc.Start(ctx) //nolint:errcheck
	}()
	<-startedCh
	time.Sleep(20 * time.Millisecond)

	go func() {
		time.Sleep(30 * time.Millisecond)
		// Publish a terminal status (no result message).
		publishStatus(t, nc, "task-status-only", a2a.TaskStateFailed)
	}()

	result, err := rc.WaitFor(ctx, "task-status-only")
	if err != nil {
		t.Fatalf("WaitFor returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result from terminal status")
	}
	if result.Status != a2a.TaskStateFailed {
		t.Errorf("expected status %q, got %q", a2a.TaskStateFailed, result.Status)
	}
}
