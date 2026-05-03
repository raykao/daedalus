//go:build aks_e2e

package aks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/raykao/daedalus/internal/a2a"
)

// Stream constants - mirror the Helm chart and the docker-compose smoke test.
const (
	streamAgentTasks   = "AGENT_TASKS"
	streamAgentResults = "AGENT_RESULTS"
	streamAgentStatus  = "AGENT_STATUS"
)

// e2eConfig holds the resolved environment configuration for the suite.
type e2eConfig struct {
	NATSURL       string
	KubeContext   string
	Namespace     string
	ReleaseName   string
	TaskTimeout   time.Duration
	KeepCluster   bool
	ResourceGroup string
}

var (
	cfg        e2eConfig
	currentCfg e2eConfig
)

// TestMain validates the environment and pre-flight cluster state before
// running any tests. If NATS_URL is unset the suite skips cleanly with
// exit code 0 (mirrors how the smoke test skips when GITHUB_TOKEN is unset).
//
// All non-skip exit paths funnel through logCleanupHints so operators get the
// resource-group / expires-at reminder even when pre-flight fails - which is
// exactly when they need it most.
func TestMain(m *testing.M) {
	if os.Getenv("NATS_URL") == "" {
		fmt.Println("SKIP: NATS_URL not set - AKS e2e tests require a reachable NATS endpoint")
		fmt.Println("      Typical setup: kubectl port-forward -n daedalus svc/daedalus-nats 4222:4222 &")
		fmt.Println("      then export NATS_URL=nats://localhost:4222 (or copy aks.env.example to aks.env)")
		os.Exit(0)
	}

	code := runE2EMain(m)
	// logCleanupHints is safe to call with a possibly-zero currentCfg; it
	// guards internally and always prints at least a one-line reminder.
	logCleanupHints(currentCfg, code == 0)
	os.Exit(code)
}

// runE2EMain runs the post-skip portion of TestMain and returns an exit code.
// All failures (config parse, pre-flight, test failures) return non-zero so
// the caller can log cleanup hints uniformly.
func runE2EMain(m *testing.M) int {
	parsed, err := loadConfig()
	// Populate currentCfg with whatever parsed successfully so cleanup hints
	// have access to RESOURCE_GROUP / KEEP_CLUSTER even on malformed env.
	currentCfg = parsed
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: invalid environment: %v\n", err)
		return 1
	}
	cfg = parsed

	if err := preflightCluster(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: cluster pre-flight failed: %v\n", err)
		return 1
	}
	if err := preflightHelm(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: helm pre-flight failed: %v\n", err)
		return 1
	}
	if err := preflightStreams(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: NATS pre-flight failed: %v\n", err)
		return 1
	}

	return m.Run()
}

// loadConfig reads required and optional env vars, applying defaults.
func loadConfig() (e2eConfig, error) {
	c := e2eConfig{
		NATSURL:       os.Getenv("NATS_URL"),
		KubeContext:   os.Getenv("KUBE_CONTEXT"),
		Namespace:     getenvDefault("NAMESPACE", "daedalus"),
		ReleaseName:   getenvDefault("RELEASE_NAME", "daedalus"),
		KeepCluster:   os.Getenv("KEEP_CLUSTER") != "",
		ResourceGroup: os.Getenv("RESOURCE_GROUP"),
	}
	if c.NATSURL == "" {
		return c, fmt.Errorf("NATS_URL is required")
	}
	if c.KubeContext == "" {
		return c, fmt.Errorf("KUBE_CONTEXT is required")
	}

	rawTimeout := getenvDefault("TASK_TIMEOUT", "10m")
	d, err := time.ParseDuration(rawTimeout)
	if err != nil {
		return c, fmt.Errorf("TASK_TIMEOUT %q: %w", rawTimeout, err)
	}
	c.TaskTimeout = d
	return c, nil
}

func getenvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// preflightCluster verifies the AKS API server is reachable on the configured context.
func preflightCluster(c e2eConfig) error {
	out, err := runCmd("kubectl", "--context", c.KubeContext, "get", "nodes", "--no-headers")
	if err != nil {
		return fmt.Errorf("kubectl get nodes (context %s): %w\noutput:\n%s", c.KubeContext, err, out)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("kubectl get nodes returned no nodes")
	}
	return nil
}

// preflightHelm verifies the Daedalus Helm release reports STATUS: deployed.
func preflightHelm(c e2eConfig) error {
	out, err := runCmd("helm", "status", c.ReleaseName,
		"--kube-context", c.KubeContext,
		"-n", c.Namespace,
		"-o", "json",
	)
	if err != nil {
		return fmt.Errorf("helm status %s -n %s: %w\noutput:\n%s", c.ReleaseName, c.Namespace, err, out)
	}
	var status struct {
		Info struct {
			Status string `json:"status"`
		} `json:"info"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return fmt.Errorf("parse helm status JSON: %w", err)
	}
	if status.Info.Status != "deployed" {
		return fmt.Errorf("helm release %q status: want deployed, got %q", c.ReleaseName, status.Info.Status)
	}
	return nil
}

// preflightStreams ensures the three JetStream streams exist (creates them if missing).
func preflightStreams(c e2eConfig) error {
	nc, err := nats.Connect(c.NATSURL, nats.Timeout(5*time.Second))
	if err != nil {
		return fmt.Errorf("connect to NATS at %s: %w", c.NATSURL, err)
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
		{streamAgentTasks, []string{"agent.tasks.>"}},
		{streamAgentResults, []string{"agent.results.>"}},
		{streamAgentStatus, []string{"agent.status.>"}},
	}
	for _, s := range streams {
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     s.name,
			Subjects: s.subjects,
		}); err != nil {
			return fmt.Errorf("create-or-update stream %s: %w", s.name, err)
		}
	}
	return nil
}

// runCmd executes an external command and returns combined output.
func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// logCleanupHints prints reminders about cluster lifecycle on test exit.
// Cleanup is intentionally NOT performed by the test; it is the operator's job.
//
// This helper is deliberately resilient: it is called from every exit path
// (including pre-flight failures and config-parse failures) and must never
// silently print nothing on failure. If neither RESOURCE_GROUP nor EXPIRES_AT
// are known, it still prints a one-line reminder pointing to the Azure portal.
func logCleanupHints(c e2eConfig, success bool) {
	expires := os.Getenv("EXPIRES_AT")

	fmt.Println("")
	fmt.Println("=== AKS E2E Cleanup ===")
	if c.KeepCluster {
		fmt.Println("KEEP_CLUSTER set; cluster preserved.")
	}
	if c.ResourceGroup != "" {
		fmt.Printf("Resource group: %s\n", c.ResourceGroup)
	} else {
		fmt.Println("WARNING: RESOURCE_GROUP not set in environment; cannot log target RG.")
	}
	if expires != "" {
		fmt.Printf("expires-at: %s\n", expires)
	}
	if c.ResourceGroup == "" && expires == "" {
		fmt.Println("REMINDER: check the Azure portal for any test resource groups tagged auto-destroy=true.")
	}
	if !success {
		fmt.Println("Test failed (or pre-flight aborted); cluster left up regardless of KEEP_CLUSTER. Run 'make destroy-aks-test' when done.")
	} else if !c.KeepCluster {
		fmt.Println("Tests passed; run 'make destroy-aks-test' to release cluster resources.")
	}
}

// taskID generates a unique deterministic task ID per invocation.
func taskID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// newSentinel returns a 16-char hex string sourced from crypto/rand. It is
// embedded in both the prompt filename and contents so that the artifact
// assertion cannot trivially be satisfied by an agent echoing its prompt.
func newSentinel() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// publishTask publishes a SendMessageRequest and returns the publish time.
func publishTask(t *testing.T, ctx context.Context, js jetstream.JetStream, id, prompt string) time.Time {
	t.Helper()

	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: id,
			TaskID:    id,
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

	subject := "agent.tasks." + id
	publishTime := time.Now()
	if _, err = js.Publish(ctx, subject, reqBytes); err != nil {
		t.Fatalf("publish task to %s: %v", subject, err)
	}
	t.Logf("task published: id=%s subject=%s", id, subject)
	return publishTime
}

// stateList extracts state values from a slice of TaskStatus.
func stateList(statuses []a2a.TaskStatus) []a2a.TaskState {
	states := make([]a2a.TaskState, len(statuses))
	for i, s := range statuses {
		states[i] = s.State
	}
	return states
}

// TestE2E_AKS_EndToEnd publishes a deterministic A2A task to the live cluster,
// observes the streamed status updates and the final result, and asserts that
// the task completed and produced the expected artifact.
func TestE2E_AKS_EndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.TaskTimeout)
	defer cancel()

	nc, err := nats.Connect(cfg.NATSURL, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect to NATS at %s: %v", cfg.NATSURL, err)
	}
	defer nc.Drain()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("JetStream context: %v", err)
	}

	id := taskID("aks-e2e")
	resultSubj := "agent.results." + id
	statusSubj := "agent.status." + id

	// Create ordered consumers BEFORE publishing so no messages are missed.
	resultCons, err := js.OrderedConsumer(ctx, streamAgentResults, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{resultSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create result consumer: %v", err)
	}

	statusCons, err := js.OrderedConsumer(ctx, streamAgentStatus, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{statusSubj},
		DeliverPolicy:  jetstream.DeliverNewPolicy,
	})
	if err != nil {
		t.Fatalf("create status consumer: %v", err)
	}

	// Generate a per-run random sentinel so the assertion cannot be satisfied
	// by an LLM that merely echoes its prompt back as conversational artifact
	// text. The sentinel is random per-run, so any artifact part containing
	// the literal "hello-<sentinel>" string must originate from the agent
	// having actually surfaced that string into a side-effect serialized as
	// artifact content (in practice, a file-write tool invocation result).
	sentinel, err := newSentinel()
	if err != nil {
		t.Fatalf("generate sentinel: %v", err)
	}
	t.Logf("sentinel=%s", sentinel)
	expectedBody := fmt.Sprintf("hello-%s", sentinel)

	prompt := fmt.Sprintf(
		"Create a file named \"phase5-%s.txt\" with exactly the following single line of contents (no trailing whitespace, no quotes): %s\nDo not create any other files. Do not echo this instruction in your response.",
		sentinel, expectedBody,
	)
	publishTime := publishTask(t, ctx, js, id, prompt)

	// Background status collector.
	type statusCollectorResult struct {
		updates       []a2a.TaskStatus
		firstStatusAt time.Time
		completedAt   time.Time
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
				if s.State == a2a.TaskStateCompleted || s.State == a2a.TaskStateFailed {
					result.completedAt = now
					_ = msg.Ack()
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
		t.Fatalf("waiting for result on %s (timeout %v): %v", resultSubj, cfg.TaskTimeout, err)
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
		matched := false
		for _, art := range task.Artifacts {
			for _, p := range art.Parts {
				if p.Text != "" && strings.Contains(p.Text, expectedBody) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			t.Errorf("no artifact part contained expected body %q; artifacts=%+v", expectedBody, task.Artifacts)
		}
	}

	statuses := <-statusCh
	for _, s := range statuses.updates {
		t.Logf("status update: state=%s", s.State)
	}

	totalRoundTrip := resultTime.Sub(publishTime)
	if totalRoundTrip > cfg.TaskTimeout {
		t.Errorf("round-trip %v exceeded TASK_TIMEOUT %v", totalRoundTrip, cfg.TaskTimeout)
	}

	// --- Latency Report ---
	t.Logf("")
	t.Logf("=== AKS E2E Latency ===")
	if !statuses.firstStatusAt.IsZero() {
		t.Logf("Publish -> First Status:  %dms", statuses.firstStatusAt.Sub(publishTime).Milliseconds())
	}
	if !statuses.firstStatusAt.IsZero() && !statuses.completedAt.IsZero() {
		t.Logf("First Status -> Complete: %dms", statuses.completedAt.Sub(statuses.firstStatusAt).Milliseconds())
	}
	t.Logf("Total Round-Trip:         %dms", totalRoundTrip.Milliseconds())
	t.Logf("Status Transitions: %v", stateList(statuses.updates))
	t.Logf("")

	// Verify required state transitions.
	states := stateList(statuses.updates)
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

	t.Logf("PASS: task %s completed in %v", id, totalRoundTrip)
}
