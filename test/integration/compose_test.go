//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/raykao/agent-forge/internal/a2a"
)

const (
	natsURL       = "nats://localhost:4222"
	testTaskID    = "test-task-1"
	taskSubject   = "agent.tasks." + testTaskID
	resultSubject = "agent.results." + testTaskID
	statusSubject = "agent.status." + testTaskID
	resultStream  = "AGENT_RESULTS"
	statusStream  = "AGENT_STATUS"
	taskStream    = "AGENT_TASKS"
)

var composeFile string

func TestMain(m *testing.M) {
	// Locate the compose file relative to this source file.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "could not determine test file location")
		os.Exit(1)
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	composeFile = filepath.Join(repoRoot, "deploy", "docker", "docker-compose.yml")

	if err := buildImages(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose build failed: %v\n", err)
		os.Exit(1)
	}
	if err := startStack(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up failed: %v\n", err)
		os.Exit(1)
	}
	if err := waitForNATS(60 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "NATS not ready: %v\n", err)
		teardown()
		os.Exit(1)
	}

	// Create streams before running tests. These must exist before the proxy
	// attempts to publish results or status updates.
	if err := ensureStreams(); err != nil {
		fmt.Fprintf(os.Stderr, "stream setup failed: %v\n", err)
		teardown()
		os.Exit(1)
	}

	code := m.Run()
	teardown()
	os.Exit(code)
}

func buildImages() error {
	cmd := exec.Command("docker", "compose", "-f", composeFile, "build")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startStack() error {
	cmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func teardown() {
	cmd := exec.Command("docker", "compose", "-f", composeFile, "down", "-v")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func ensureStreams() error {
	nc, err := nats.Connect(natsURL, nats.Timeout(5*time.Second))
	if err != nil {
		return fmt.Errorf("connect to NATS: %w", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream context: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Ensure all three streams exist before any test or proxy publish attempt.
	streams := []struct {
		name     string
		subjects []string
	}{
		{taskStream, []string{"agent.tasks.>"}},
		{resultStream, []string{"agent.results.>"}},
		{statusStream, []string{"agent.status.>"}},
	}
	for _, s := range streams {
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     s.name,
			Subjects: s.subjects,
		}); err != nil {
			return fmt.Errorf("create stream %s: %w", s.name, err)
		}
	}
	return nil
}

func waitForNATS(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nc, err := nats.Connect(natsURL, nats.Timeout(2*time.Second))
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		js, err := jetstream.New(nc)
		nc.Close()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err = js.AccountInfo(ctx)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("NATS not ready after %v", timeout)
}

// TestEndToEnd_CompletedTask publishes an A2A task to NATS, waits for the proxy
// to drive the mock ACP agent, and asserts the result has state "completed" with
// non-empty artifact text. It also verifies that status updates include both
// "working" and "completed" states and logs end-to-end latency.
func TestEndToEnd_CompletedTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	nc, err := nats.Connect(natsURL, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Drain()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("JetStream context: %v", err)
	}

	// Set up ordered consumers BEFORE publishing so no messages are missed.
	resultCons, err := js.OrderedConsumer(ctx, resultStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{resultSubject},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create result consumer: %v", err)
	}

	statusCons, err := js.OrderedConsumer(ctx, statusStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{statusSubject},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create status consumer: %v", err)
	}

	// Publish the A2A task.
	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: testTaskID,
			TaskID:    testTaskID,
			Role:      "user",
			Parts: []a2a.Part{
				{Text: "Hello from integration test. Please respond to confirm you are working."},
			},
		},
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	publishStart := time.Now()
	if _, err = js.Publish(ctx, taskSubject, reqBytes); err != nil {
		t.Fatalf("publish task to %s: %v", taskSubject, err)
	}
	t.Logf("task published: subject=%s time=%v", taskSubject, publishStart.Format(time.RFC3339))

	// Collect status updates in the background (working -> completed).
	type statusResult struct {
		updates []a2a.TaskStatus
	}
	statusCh := make(chan statusResult, 1)
	go func() {
		var updates []a2a.TaskStatus
		for {
			// Check if context is done before attempting fetch.
			select {
			case <-ctx.Done():
				statusCh <- statusResult{updates: updates}
				return
			default:
			}

			msg, fetchErr := statusCons.Next(
				jetstream.FetchContext(ctx),
			)
			if fetchErr != nil {
				statusCh <- statusResult{updates: updates}
				return
			}
			var s a2a.TaskStatus
			if jsonErr := json.Unmarshal(msg.Data(), &s); jsonErr == nil {
				updates = append(updates, s)
				t.Logf("status update received: state=%s", s.State)
				if s.State == a2a.TaskStateCompleted || s.State == a2a.TaskStateFailed {
					statusCh <- statusResult{updates: updates}
					return
				}
			}
			_ = msg.Ack()
		}
	}()

	// Wait for the result message.
	resultMsg, err := resultCons.Next(
		jetstream.FetchContext(ctx),
	)
	if err != nil {
		t.Fatalf("waiting for result on %s: %v", resultSubject, err)
	}
	latency := time.Since(publishStart)
	t.Logf("end-to-end latency: %v", latency)

	var task a2a.Task
	if err := json.Unmarshal(resultMsg.Data(), &task); err != nil {
		t.Fatalf("unmarshal result Task: %v", err)
	}
	_ = resultMsg.Ack()

	// --- Assertions ---

	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("task state: want completed, got %q", task.Status.State)
	}

	if len(task.Artifacts) == 0 {
		t.Errorf("expected at least one artifact in result, got none")
	} else {
		hasText := false
		for _, p := range task.Artifacts[0].Parts {
			if p.Text != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			t.Errorf("artifact[0] has no text: parts=%+v", task.Artifacts[0].Parts)
		}
	}

	if task.ID != testTaskID {
		t.Errorf("task ID: want %q, got %q", testTaskID, task.ID)
	}

	// Wait for the status goroutine to finish (with a short grace period).
	var statuses []a2a.TaskStatus
	select {
	case sr := <-statusCh:
		statuses = sr.updates
	case <-time.After(10 * time.Second):
		t.Log("status collector timed out - partial results used for assertions")
	}

	t.Logf("status updates received: count=%d", len(statuses))

	hasWorking := false
	hasCompleted := false
	for _, s := range statuses {
		switch s.State {
		case a2a.TaskStateWorking:
			hasWorking = true
		case a2a.TaskStateCompleted:
			hasCompleted = true
		}
	}

	if !hasWorking {
		t.Errorf("expected a 'working' status update; states seen: %v", stateList(statuses))
	}
	if !hasCompleted {
		t.Errorf("expected a 'completed' status update; states seen: %v", stateList(statuses))
	}

	t.Logf("PASS: task %s completed in %v (status updates: %v)", testTaskID, latency, stateList(statuses))
}

func stateList(statuses []a2a.TaskStatus) []a2a.TaskState {
	states := make([]a2a.TaskState, len(statuses))
	for i, s := range statuses {
		states[i] = s.State
	}
	return states
}
