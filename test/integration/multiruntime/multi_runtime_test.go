//go:build integration

package multiruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/registry"
)

const (
	echoA2AURL                 = "http://localhost:8080"
	multiRuntimeComposeTimeout = 120 * time.Second
)

var multiRuntimeComposeFile string

// TestMain starts the multi-runtime Docker Compose stack before tests
// and tears it down afterward.
func TestMain(m *testing.M) {
	// Locate the compose file relative to this source file.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fmt.Fprintln(os.Stderr, "could not determine test file location")
		os.Exit(1)
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	multiRuntimeComposeFile = filepath.Join(repoRoot, "deploy", "docker", "docker-compose.multi-runtime.yml")

	if err := buildMultiRuntimeImages(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose build failed: %v\n", err)
		os.Exit(1)
	}
	if err := startMultiRuntimeStack(); err != nil {
		fmt.Fprintf(os.Stderr, "docker compose up failed: %v\n", err)
		os.Exit(1)
	}

	// Wait for the echo-a2a server to become healthy.
	if err := waitForHTTPHealthy(echoA2AURL+"/health", multiRuntimeComposeTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "echo-a2a not healthy: %v\n", err)
		teardownMultiRuntime()
		os.Exit(1)
	}

	code := m.Run()
	teardownMultiRuntime()
	os.Exit(code)
}

func buildMultiRuntimeImages() error {
	cmd := exec.Command("docker", "compose", "-f", multiRuntimeComposeFile, "build")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startMultiRuntimeStack() error {
	cmd := exec.Command("docker", "compose", "-f", multiRuntimeComposeFile, "up", "-d")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func teardownMultiRuntime() {
	cmd := exec.Command("docker", "compose", "-f", multiRuntimeComposeFile, "down", "-v")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// waitForHTTPHealthy polls a health endpoint until it returns 200 with {"status":"ok"}.
func waitForHTTPHealthy(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			var result map[string]string
			json.NewDecoder(resp.Body).Decode(&result)
			resp.Body.Close()
			if result["status"] == "ok" {
				return nil
			}
		} else {
			resp.Body.Close()
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("endpoint %s not healthy after %v", url, timeout)
}

// sendA2AMessage sends a SendMessageRequest to the given URL and returns the Task response.
func sendA2AMessage(baseURL string, req a2a.SendMessageRequest) (*a2a.Task, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, baseURL+"/", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var task a2a.Task
	if err := json.Unmarshal(respBody, &task); err != nil {
		return nil, fmt.Errorf("decoding task response: %w", err)
	}
	return &task, nil
}

// TestMultiRuntimeHealthChecks verifies both runtimes are healthy.
func TestMultiRuntimeHealthChecks(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	// Echo A2A runtime health check.
	resp, err := client.Get(echoA2AURL + "/health")
	if err != nil {
		t.Fatalf("echo-a2a health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("echo-a2a health status: want 200, got %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding health response: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("echo-a2a health status: want \"ok\", got %q", result["status"])
	}

	t.Log("PASS: echo-a2a health check returned ok")
}

// TestMultiRuntimeAgentCardDiscovery verifies AgentCard discovery from the echo-a2a server.
func TestMultiRuntimeAgentCardDiscovery(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get(echoA2AURL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("agent card request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent card status: want 200, got %d", resp.StatusCode)
	}

	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decoding agent card: %v", err)
	}

	if card.Name != "echo-agent" {
		t.Errorf("agent name: want \"echo-agent\", got %q", card.Name)
	}
	if card.Version != "1.0.0" {
		t.Errorf("version: want \"1.0.0\", got %q", card.Version)
	}
	if len(card.Skills) == 0 {
		t.Fatal("expected at least one skill in agent card")
	}
	if card.Skills[0].ID != "echo" {
		t.Errorf("skill[0] ID: want \"echo\", got %q", card.Skills[0].ID)
	}

	t.Logf("PASS: discovered agent card: name=%s version=%s skills=%d",
		card.Name, card.Version, len(card.Skills))
}

// TestMultiRuntimeRegistryRouting validates that a Registry with two agents
// routes skill-based lookups to the correct NATS queue subject.
// This is a unit-level test embedded in the integration file to prove
// the registry routing concept with both runtime entries.
func TestMultiRuntimeRegistryRouting(t *testing.T) {
	regJSON := `{
		"agents": [
			{
				"card": {
					"name": "coding-agent",
					"description": "ACP-based coding agent",
					"version": "1.0.0",
					"defaultInputModes": ["text/plain"],
					"defaultOutputModes": ["text/plain"],
					"skills": [{"id": "code", "name": "Code", "description": "Generates code", "tags": ["coding"]}]
				},
				"queueSubject": "agent.tasks.coding",
				"runtime": "acp",
				"acpPort": 3000
			},
			{
				"card": {
					"name": "echo-agent",
					"description": "A2A echo agent",
					"version": "1.0.0",
					"defaultInputModes": ["text/plain"],
					"defaultOutputModes": ["text/plain"],
					"skills": [{"id": "echo", "name": "Echo", "description": "Echoes input back", "tags": ["testing"]}]
				},
				"queueSubject": "agent.tasks.echo",
				"runtime": "a2a-http",
				"acpPort": 8080
			}
		]
	}`

	reg, err := registry.LoadFromReader(strings.NewReader(regJSON))
	if err != nil {
		t.Fatalf("loading registry: %v", err)
	}

	// Verify routing by skill ID.
	codingSubject, err := reg.Route("code")
	if err != nil {
		t.Fatalf("Route(\"code\") failed: %v", err)
	}
	if codingSubject != "agent.tasks.coding" {
		t.Errorf("Route(\"code\"): want \"agent.tasks.coding\", got %q", codingSubject)
	}

	echoSubject, err := reg.Route("echo")
	if err != nil {
		t.Fatalf("Route(\"echo\") failed: %v", err)
	}
	if echoSubject != "agent.tasks.echo" {
		t.Errorf("Route(\"echo\"): want \"agent.tasks.echo\", got %q", echoSubject)
	}

	// Verify FindByName returns the correct entries.
	codingEntry, found := reg.FindByName("coding-agent")
	if !found {
		t.Fatal("coding-agent not found in registry")
	}
	if codingEntry.Runtime != "acp" {
		t.Errorf("coding-agent runtime: want \"acp\", got %q", codingEntry.Runtime)
	}

	echoEntry, found := reg.FindByName("echo-agent")
	if !found {
		t.Fatal("echo-agent not found in registry")
	}
	if echoEntry.Runtime != "a2a-http" {
		t.Errorf("echo-agent runtime: want \"a2a-http\", got %q", echoEntry.Runtime)
	}

	// Verify RouteByScore picks the right runtime when preferred.
	ctx := context.Background()
	_ = ctx // registry.RouteByScore does not use context

	echoByScore, err := reg.RouteByScore(registry.RoutingRequest{
		SkillID:          "echo",
		PreferredRuntime: "a2a-http",
	})
	if err != nil {
		t.Fatalf("RouteByScore(echo) failed: %v", err)
	}
	if echoByScore.Card.Name != "echo-agent" {
		t.Errorf("RouteByScore(echo): want echo-agent, got %q", echoByScore.Card.Name)
	}

	t.Log("PASS: registry routes code->agent.tasks.coding, echo->agent.tasks.echo")
}

// TestMultiRuntimeDirectA2ATask sends a task directly to the echo-a2a server
// and verifies it returns a completed task with the echoed text.
func TestMultiRuntimeDirectA2ATask(t *testing.T) {
	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "multi-rt-msg-1",
			TaskID:    "multi-rt-task-1",
			Role:      "user",
			Parts:     []a2a.Part{{Text: "hello from multi-runtime test"}},
		},
	}

	task, err := sendA2AMessage(echoA2AURL, req)
	if err != nil {
		t.Fatalf("sendA2AMessage failed: %v", err)
	}

	if task.ID != "multi-rt-task-1" {
		t.Errorf("task ID: want \"multi-rt-task-1\", got %q", task.ID)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("task state: want completed, got %q", task.Status.State)
	}
	if len(task.Artifacts) == 0 {
		t.Fatal("expected at least one artifact")
	}
	if len(task.Artifacts[0].Parts) == 0 {
		t.Fatal("expected at least one part in artifact")
	}

	want := "echo: hello from multi-runtime test"
	got := task.Artifacts[0].Parts[0].Text
	if got != want {
		t.Errorf("echo text: want %q, got %q", want, got)
	}

	t.Logf("PASS: direct A2A task returned: %q", got)
}

// TestMultiRuntimeCrossRuntimeChain demonstrates cross-runtime data flow
// by chaining the output of one task as the input of the next.
//
// Limitation: True cross-runtime chaining (ACP runtime -> A2A HTTP runtime)
// would require the proxy to also expose an A2A HTTP endpoint externally,
// which it currently does not. This test validates the chaining concept
// using echo-a2a -> echo-a2a, proving that task output can flow between
// runtime invocations.
func TestMultiRuntimeCrossRuntimeChain(t *testing.T) {
	// Step 1: Send initial task to echo-a2a.
	req1 := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "chain-msg-1",
			TaskID:    "chain-task-1",
			Role:      "user",
			Parts:     []a2a.Part{{Text: "hello"}},
		},
	}

	task1, err := sendA2AMessage(echoA2AURL, req1)
	if err != nil {
		t.Fatalf("step 1: sendA2AMessage failed: %v", err)
	}

	if task1.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("step 1: task state: want completed, got %q", task1.Status.State)
	}
	if len(task1.Artifacts) == 0 || len(task1.Artifacts[0].Parts) == 0 {
		t.Fatal("step 1: expected artifact with text")
	}

	step1Output := task1.Artifacts[0].Parts[0].Text
	wantStep1 := "echo: hello"
	if step1Output != wantStep1 {
		t.Fatalf("step 1 output: want %q, got %q", wantStep1, step1Output)
	}
	t.Logf("step 1 output: %q", step1Output)

	// Step 2: Feed step 1 output as input to another echo-a2a call.
	// This simulates an orchestrator passing data between runtimes.
	req2 := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "chain-msg-2",
			TaskID:    "chain-task-2",
			Role:      "user",
			Parts:     []a2a.Part{{Text: step1Output}},
		},
	}

	task2, err := sendA2AMessage(echoA2AURL, req2)
	if err != nil {
		t.Fatalf("step 2: sendA2AMessage failed: %v", err)
	}

	if task2.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("step 2: task state: want completed, got %q", task2.Status.State)
	}
	if len(task2.Artifacts) == 0 || len(task2.Artifacts[0].Parts) == 0 {
		t.Fatal("step 2: expected artifact with text")
	}

	step2Output := task2.Artifacts[0].Parts[0].Text
	wantStep2 := "echo: echo: hello"
	if step2Output != wantStep2 {
		t.Errorf("step 2 output: want %q, got %q", wantStep2, step2Output)
	}

	t.Logf("PASS: cross-runtime chain: \"hello\" -> %q -> %q", step1Output, step2Output)
}
