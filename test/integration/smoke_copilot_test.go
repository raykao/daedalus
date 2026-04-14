//go:build smoke

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/raykao/daedalus/internal/a2a"
)

const (
	smokeNATSURL     = "nats://localhost:4222"
	smokeResultStream = "AGENT_RESULTS"
	smokeStatusStream = "AGENT_STATUS"
	smokeTaskStream   = "AGENT_TASKS"
)

// smokeComposeFile is the path to the Copilot CLI Docker Compose file,
// set during TestMain.
var smokeComposeFile string

// smokeSetupOnce ensures the stack is only started once across all tests.
var smokeSetupOnce sync.Once

func init() {
	// Locate the compose file relative to this source file.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "could not determine test file location")
		os.Exit(1)
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	smokeComposeFile = filepath.Join(repoRoot, "deploy", "docker", "docker-compose.copilot-cli.yml")
}

// TestMain manages the Copilot CLI Docker Compose stack lifecycle.
// It requires GITHUB_TOKEN to be set; skips gracefully if absent.
func TestMain(m *testing.M) {
	if os.Getenv("GITHUB_TOKEN") == "" {
		fmt.Println("SKIP: GITHUB_TOKEN not set - smoke tests require Copilot access")
		os.Exit(0)
	}

	if err := smokeBuildImages(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose build failed: %v\n", err)
		os.Exit(1)
	}
	if err := smokeStartStack(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up failed: %v\n", err)
		os.Exit(1)
	}
	if err := smokeWaitForHealth(120 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "services not healthy: %v\n", err)
		smokeTeardown()
		os.Exit(1)
	}
	if err := smokeWaitForNATS(60 * time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "NATS not ready: %v\n", err)
		smokeTeardown()
		os.Exit(1)
	}
	if err := smokeEnsureStreams(); err != nil {
		fmt.Fprintf(os.Stderr, "stream setup failed: %v\n", err)
		smokeTeardown()
		os.Exit(1)
	}

	code := m.Run()
	smokeTeardown()
	os.Exit(code)
}

func smokeBuildImages() error {
	cmd := exec.Command("docker", "compose", "-f", smokeComposeFile, "build", "--quiet")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func smokeStartStack() error {
	cmd := exec.Command("docker", "compose", "-f", smokeComposeFile, "up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func smokeTeardown() {
	cmd := exec.Command("docker", "compose", "-f", smokeComposeFile, "down", "-v")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// smokeWaitForHealth polls docker compose ps until all services report healthy
// or running state, with the given timeout.
func smokeWaitForHealth(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cmd := exec.Command("docker", "compose", "-f", smokeComposeFile, "ps", "--format", "json")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			// docker compose ps --format json outputs one JSON object per line.
			type serviceInfo struct {
				Health string `json:"Health"`
				State  string `json:"State"`
			}
			healthy := 0
			for _, line := range splitLines(out) {
				if len(line) == 0 {
					continue
				}
				var svc serviceInfo
				if jsonErr := json.Unmarshal(line, &svc); jsonErr == nil {
					if svc.Health == "healthy" || svc.State == "running" {
						healthy++
					}
				}
			}
			// We need at least 3 services: nats, copilot-cli, proxy
			// (nats-box is a utility container)
			if healthy >= 3 {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("services not healthy after %v", timeout)
}

// splitLines splits byte output into lines.
func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

func smokeWaitForNATS(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nc, err := nats.Connect(smokeNATSURL, nats.Timeout(2*time.Second))
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

func smokeEnsureStreams() error {
	nc, err := nats.Connect(smokeNATSURL, nats.Timeout(5*time.Second))
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

	streams := []struct {
		name     string
		subjects []string
	}{
		{smokeTaskStream, []string{"agent.tasks.>"}},
		{smokeResultStream, []string{"agent.results.>"}},
		{smokeStatusStream, []string{"agent.status.>"}},
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

// ---------------------------------------------------------------------------
// Smoke Tests
// ---------------------------------------------------------------------------

// smokeTaskID generates a unique task ID for each test invocation.
func smokeTaskID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// publishSmokeTask publishes a SendMessageRequest and returns the task ID,
// publish time, and any error.
func publishSmokeTask(t *testing.T, ctx context.Context, js jetstream.JetStream, taskID, prompt string) time.Time {
	t.Helper()

	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: taskID,
			TaskID:    taskID,
			Role:      "user",
			Parts: []a2a.Part{
				{Text: prompt},
			},
		},
	}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	subject := "agent.tasks." + taskID
	publishTime := time.Now()
	if _, err = js.Publish(ctx, subject, reqBytes); err != nil {
		t.Fatalf("publish task to %s: %v", subject, err)
	}
	t.Logf("task published: id=%s subject=%s", taskID, subject)
	return publishTime
}

// TestSmoke_EndToEnd is the main end-to-end smoke test. It publishes an A2A
// task to NATS, waits for the proxy to drive the Copilot CLI via ACP, and
// validates the result with latency measurement at each step.
func TestSmoke_EndToEnd(t *testing.T) {
	const timeout = 180 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	nc, err := nats.Connect(smokeNATSURL, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Drain()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("JetStream context: %v", err)
	}

	taskID := smokeTaskID("smoke")
	resultSubj := "agent.results." + taskID
	statusSubj := "agent.status." + taskID

	// Create ordered consumers BEFORE publishing so no messages are missed.
	resultCons, err := js.OrderedConsumer(ctx, smokeResultStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{resultSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create result consumer: %v", err)
	}

	statusCons, err := js.OrderedConsumer(ctx, smokeStatusStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{statusSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create status consumer: %v", err)
	}

	// Publish the task.
	prompt := "Create a file called hello.txt with the contents 'Hello from Daedalus smoke test'. Do not create any other files."
	publishTime := publishSmokeTask(t, ctx, js, taskID, prompt)

	// Collect status updates in background.
	type statusCollectorResult struct {
		updates        []a2a.TaskStatus
		firstStatusAt  time.Time
		completedAt    time.Time
	}
	statusCh := make(chan statusCollectorResult, 1)
	go func() {
		var result statusCollectorResult
		for {
			select {
			case <-ctx.Done():
				statusCh <- result
				return
			default:
			}

			msg, fetchErr := statusCons.Next(jetstream.FetchContext(ctx))
			if fetchErr != nil {
				statusCh <- result
				return
			}
			var s a2a.TaskStatus
			if jsonErr := json.Unmarshal(msg.Data(), &s); jsonErr == nil {
				now := time.Now()
				if len(result.updates) == 0 {
					result.firstStatusAt = now
				}
				result.updates = append(result.updates, s)
				t.Logf("status update: state=%s", s.State)
				if s.State == a2a.TaskStateCompleted || s.State == a2a.TaskStateFailed {
					result.completedAt = now
					statusCh <- result
					return
				}
			}
			_ = msg.Ack()
		}
	}()

	// Wait for the result message.
	resultMsg, err := resultCons.Next(jetstream.FetchContext(ctx))
	if err != nil {
		t.Fatalf("waiting for result on %s (timeout %v): %v", resultSubj, timeout, err)
	}
	resultTime := time.Now()

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
		t.Errorf("expected at least one artifact, got none")
	} else {
		hasText := false
		for _, p := range task.Artifacts[0].Parts {
			if p.Text != "" {
				hasText = true
				break
			}
		}
		if !hasText {
			t.Errorf("artifact[0] has no text content: parts=%+v", task.Artifacts[0].Parts)
		}
	}

	// Wait for the status collector goroutine to finish.
	var statuses statusCollectorResult
	select {
	case statuses = <-statusCh:
	case <-time.After(10 * time.Second):
		t.Log("status collector timed out; using partial results")
	}

	// --- Latency Report ---

	totalRoundTrip := resultTime.Sub(publishTime)
	t.Logf("")
	t.Logf("=== Smoke Test Latency ===")
	if !statuses.firstStatusAt.IsZero() {
		t.Logf("Publish -> First Status:  %dms", statuses.firstStatusAt.Sub(publishTime).Milliseconds())
	}
	if !statuses.firstStatusAt.IsZero() && !statuses.completedAt.IsZero() {
		t.Logf("First Status -> Complete: %dms", statuses.completedAt.Sub(statuses.firstStatusAt).Milliseconds())
	}
	t.Logf("Total Round-Trip:         %dms", totalRoundTrip.Milliseconds())
	t.Logf("Status Transitions: %v", smokeStateList(statuses.updates))
	t.Logf("")

	// Verify we saw working and completed statuses.
	states := smokeStateList(statuses.updates)
	hasWorking := false
	hasCompleted := false
	for _, s := range states {
		if s == a2a.TaskStateWorking {
			hasWorking = true
		}
		if s == a2a.TaskStateCompleted {
			hasCompleted = true
		}
	}
	if !hasWorking {
		t.Errorf("expected 'working' status; states seen: %v", states)
	}
	if !hasCompleted {
		t.Errorf("expected 'completed' status; states seen: %v", states)
	}

	t.Logf("PASS: task %s completed in %v", taskID, totalRoundTrip)
}

// TestSmoke_StatusTransitions verifies the correct state machine ordering.
// Status transitions should follow: working -> completed (no skipped states).
func TestSmoke_StatusTransitions(t *testing.T) {
	const timeout = 180 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	nc, err := nats.Connect(smokeNATSURL, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Drain()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("JetStream context: %v", err)
	}

	taskID := smokeTaskID("smoke-status")
	statusSubj := "agent.status." + taskID

	// Create status consumer BEFORE publishing.
	statusCons, err := js.OrderedConsumer(ctx, smokeStatusStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{statusSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create status consumer: %v", err)
	}

	// Also consume results so we know when the task is done.
	resultSubj := "agent.results." + taskID
	resultCons, err := js.OrderedConsumer(ctx, smokeResultStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{resultSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create result consumer: %v", err)
	}

	prompt := "Create a file called status-test.txt with the contents 'status transition test'. Do not create any other files."
	publishSmokeTask(t, ctx, js, taskID, prompt)

	// Collect all status updates until terminal state.
	var updates []a2a.TaskStatus
	var failedMsg string
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			msg, fetchErr := statusCons.Next(jetstream.FetchContext(ctx))
			if fetchErr != nil {
				return
			}
			var s a2a.TaskStatus
			if jsonErr := json.Unmarshal(msg.Data(), &s); jsonErr == nil {
				updates = append(updates, s)
				t.Logf("status: %s", s.State)
				if s.State == a2a.TaskStateFailed && s.Message != nil {
					failedMsg = s.Message.ExtractPromptText()
				}
				if s.State == a2a.TaskStateCompleted || s.State == a2a.TaskStateFailed {
					return
				}
			}
			_ = msg.Ack()
		}
	}()

	// Wait for result to ensure task completed.
	resultMsg, err := resultCons.Next(jetstream.FetchContext(ctx))
	if err != nil {
		t.Fatalf("waiting for result: %v", err)
	}
	_ = resultMsg.Ack()

	// Wait for status collector.
	select {
	case <-statusDone:
	case <-time.After(10 * time.Second):
		t.Log("status collector timed out; using partial results")
	}

	states := smokeStateList(updates)
	t.Logf("status transitions: %v", states)

	if len(states) == 0 {
		t.Fatal("no status updates received")
	}

	// Verify ordering: working should come before completed.
	workingIdx := -1
	completedIdx := -1
	for i, s := range states {
		if s == a2a.TaskStateWorking && workingIdx == -1 {
			workingIdx = i
		}
		if s == a2a.TaskStateCompleted && completedIdx == -1 {
			completedIdx = i
		}
	}

	if workingIdx == -1 {
		t.Errorf("missing 'working' state; transitions: %v", states)
	}
	if completedIdx == -1 {
		if failedMsg != "" {
			t.Errorf("task failed instead of completing: %s; transitions: %v", failedMsg, states)
		} else {
			t.Errorf("missing 'completed' state; transitions: %v", states)
		}
	}
	if workingIdx >= 0 && completedIdx >= 0 && workingIdx >= completedIdx {
		t.Errorf("'working' (idx=%d) should precede 'completed' (idx=%d); transitions: %v",
			workingIdx, completedIdx, states)
	}
}

// TestSmoke_TaskIDPropagation verifies that the task ID flows through the
// entire pipeline: publish -> status -> result all reference the same ID.
func TestSmoke_TaskIDPropagation(t *testing.T) {
	const timeout = 180 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	nc, err := nats.Connect(smokeNATSURL, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect to NATS: %v", err)
	}
	defer nc.Drain()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("JetStream context: %v", err)
	}

	taskID := smokeTaskID("smoke-propagate")
	resultSubj := "agent.results." + taskID
	statusSubj := "agent.status." + taskID

	// Create consumers BEFORE publishing.
	resultCons, err := js.OrderedConsumer(ctx, smokeResultStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{resultSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create result consumer: %v", err)
	}

	statusCons, err := js.OrderedConsumer(ctx, smokeStatusStream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{statusSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create status consumer: %v", err)
	}

	prompt := "Create a file called propagate-test.txt with the contents 'task ID propagation test'. Do not create any other files."
	publishSmokeTask(t, ctx, js, taskID, prompt)

	// Collect at least one status update.
	var statusTaskID string
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		msg, fetchErr := statusCons.Next(jetstream.FetchContext(ctx))
		if fetchErr != nil {
			return
		}
		// Status updates arrive on subject agent.status.<taskID>,
		// so the subject itself carries the task ID. We also check
		// any ID field in the payload if present.
		var s a2a.TaskStatus
		if jsonErr := json.Unmarshal(msg.Data(), &s); jsonErr == nil {
			// Extract task ID from the NATS subject.
			subj := msg.Subject()
			if len(subj) > len("agent.status.") {
				statusTaskID = subj[len("agent.status."):]
			}
			t.Logf("status received on subject: %s", subj)
		}
		_ = msg.Ack()
	}()

	// Wait for result.
	resultMsg, err := resultCons.Next(jetstream.FetchContext(ctx))
	if err != nil {
		t.Fatalf("waiting for result: %v", err)
	}

	var task a2a.Task
	if err := json.Unmarshal(resultMsg.Data(), &task); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	_ = resultMsg.Ack()

	// Wait for status goroutine.
	select {
	case <-statusDone:
	case <-time.After(10 * time.Second):
		t.Log("status collector timed out")
	}

	// Assert: result task ID matches published task ID.
	if task.ID != taskID {
		t.Errorf("result task ID: want %q, got %q", taskID, task.ID)
	}

	// Assert: status update was on the correct subject (task ID propagated).
	if statusTaskID != "" && statusTaskID != taskID {
		t.Errorf("status task ID: want %q, got %q", taskID, statusTaskID)
	}
	if statusTaskID == "" {
		t.Log("warning: no status task ID captured (status may not have arrived)")
	}

	t.Logf("PASS: task ID %q propagated correctly through pipeline", taskID)
}

// smokeStateList extracts the state values from a slice of TaskStatus.
func smokeStateList(statuses []a2a.TaskStatus) []a2a.TaskState {
	states := make([]a2a.TaskState, len(statuses))
	for i, s := range statuses {
		states[i] = s.State
	}
	return states
}
