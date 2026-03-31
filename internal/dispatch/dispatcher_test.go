package dispatch_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/raykao/agent-forge/internal/dispatch"
	"github.com/raykao/agent-forge/internal/registry"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newJSONReader wraps a byte slice as an io.Reader for registry.LoadFromReader.
func newJSONReader(data []byte) *jsonReaderImpl {
	return &jsonReaderImpl{data: data}
}

type jsonReaderImpl struct {
	data []byte
	pos  int
}

func (r *jsonReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, nil
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func buildRegistry(t *testing.T, entries []registry.AgentEntry) *registry.Registry {
	t.Helper()
	type regFile struct {
		Agents []registry.AgentEntry `json:"agents"`
	}
	data, err := json.Marshal(regFile{Agents: entries})
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	reg, err := registry.LoadFromReader(newJSONReader(data))
	if err != nil {
		t.Fatalf("LoadFromReader: %v", err)
	}
	return reg
}

// testAgent returns a minimal AgentEntry with the given name, skillID, and subject.
func testAgent(name, skillID, subject string) registry.AgentEntry {
	return registry.AgentEntry{
		Card: a2a.AgentCard{
			Name:        name,
			Description: "test agent",
			Version:     "1.0.0",
			Skills: []a2a.AgentSkill{
				{ID: skillID, Name: "skill-" + skillID},
			},
			DefaultInputModes:  []string{"text"},
			DefaultOutputModes: []string{"text"},
		},
		QueueSubject: subject,
		Runtime:      "docker",
		ACPPort:      8080,
	}
}

// ---------------------------------------------------------------------------
// mockPublisher records published messages.
// ---------------------------------------------------------------------------

type publishedMessage struct {
	subject string
	data    []byte
}

type mockPublisher struct {
	mu        sync.Mutex
	published []publishedMessage
	returnErr error
}

func (m *mockPublisher) PublishJSON(ctx context.Context, subject string, v interface{}) error {
	if m.returnErr != nil {
		return m.returnErr
	}
	data, _ := json.Marshal(v)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.published = append(m.published, publishedMessage{subject: subject, data: data})
	return nil
}

func (m *mockPublisher) messages() []publishedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]publishedMessage, len(m.published))
	copy(out, m.published)
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDispatch_SuccessRoutesToCorrectSubject(t *testing.T) {
	reg := buildRegistry(t, []registry.AgentEntry{
		testAgent("agent-a", "summarize", "agent.tasks.summarize"),
	})

	pub := &mockPublisher{}
	d := dispatch.NewDispatcher(pub, reg, nil)

	taskID, subject, err := d.Dispatch(context.Background(), dispatch.TaskSpec{
		SkillID: "summarize",
		Prompt:  "Summarize this document.",
	}, "ctx-001")

	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if subject != "agent.tasks.summarize" {
		t.Errorf("expected subject %q, got %q", "agent.tasks.summarize", subject)
	}
	if taskID == "" {
		t.Error("expected non-empty taskID")
	}

	msgs := pub.messages()
	if len(msgs) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(msgs))
	}
	if msgs[0].subject != "agent.tasks.summarize" {
		t.Errorf("published to wrong subject %q", msgs[0].subject)
	}
}

func TestDispatch_UnknownSkillReturnsError(t *testing.T) {
	reg := buildRegistry(t, []registry.AgentEntry{
		testAgent("agent-a", "summarize", "agent.tasks.summarize"),
	})

	pub := &mockPublisher{}
	d := dispatch.NewDispatcher(pub, reg, nil)

	_, _, err := d.Dispatch(context.Background(), dispatch.TaskSpec{
		SkillID: "nonexistent-skill",
		Prompt:  "Do something.",
	}, "ctx-002")

	if err == nil {
		t.Fatal("expected error for unknown skill, got nil")
	}
}

func TestDispatch_GeneratesTaskIDWhenEmpty(t *testing.T) {
	reg := buildRegistry(t, []registry.AgentEntry{
		testAgent("agent-b", "translate", "agent.tasks.translate"),
	})

	pub := &mockPublisher{}
	d := dispatch.NewDispatcher(pub, reg, nil)

	spec := dispatch.TaskSpec{
		// ID intentionally left empty
		SkillID: "translate",
		Prompt:  "Translate to French.",
	}

	taskID, _, err := d.Dispatch(context.Background(), spec, "ctx-003")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if taskID == "" {
		t.Error("expected generated taskID, got empty string")
	}
	// Generated ID should contain the contextID as prefix.
	if len(taskID) <= len("ctx-003") {
		t.Errorf("generated taskID %q looks too short", taskID)
	}
}

func TestDispatch_UsesProvidedTaskID(t *testing.T) {
	reg := buildRegistry(t, []registry.AgentEntry{
		testAgent("agent-c", "classify", "agent.tasks.classify"),
	})

	pub := &mockPublisher{}
	d := dispatch.NewDispatcher(pub, reg, nil)

	spec := dispatch.TaskSpec{
		ID:      "my-explicit-id",
		SkillID: "classify",
		Prompt:  "Classify this text.",
	}

	taskID, _, err := d.Dispatch(context.Background(), spec, "ctx-004")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if taskID != "my-explicit-id" {
		t.Errorf("expected taskID %q, got %q", "my-explicit-id", taskID)
	}
}

func TestDispatch_BuildsCorrectA2AMessage(t *testing.T) {
	reg := buildRegistry(t, []registry.AgentEntry{
		testAgent("agent-d", "extract", "agent.tasks.extract"),
	})

	pub := &mockPublisher{}
	d := dispatch.NewDispatcher(pub, reg, nil)

	spec := dispatch.TaskSpec{
		ID:      "task-xyz",
		SkillID: "extract",
		Prompt:  "Extract entities from this text.",
		Metadata: map[string]any{
			"source": "document-1",
		},
	}

	_, _, err := d.Dispatch(context.Background(), spec, "ctx-005")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	msgs := pub.messages()
	if len(msgs) == 0 {
		t.Fatal("no messages published")
	}

	var req a2a.SendMessageRequest
	if err := json.Unmarshal(msgs[0].data, &req); err != nil {
		t.Fatalf("unmarshal SendMessageRequest: %v", err)
	}

	msg := req.Message
	if msg.TaskID != "task-xyz" {
		t.Errorf("expected TaskID %q, got %q", "task-xyz", msg.TaskID)
	}
	if msg.ContextID != "ctx-005" {
		t.Errorf("expected ContextID %q, got %q", "ctx-005", msg.ContextID)
	}
	if msg.Role != "user" {
		t.Errorf("expected Role %q, got %q", "user", msg.Role)
	}
	if len(msg.Parts) == 0 || msg.Parts[0].Text != "Extract entities from this text." {
		t.Errorf("unexpected parts: %v", msg.Parts)
	}
	if msg.Metadata["source"] != "document-1" {
		t.Errorf("expected metadata source=document-1, got %v", msg.Metadata)
	}
}

func TestDispatch_PublishFailureReturnsError(t *testing.T) {
	reg := buildRegistry(t, []registry.AgentEntry{
		testAgent("agent-e", "summarize", "agent.tasks.summarize"),
	})

	pub := &mockPublisher{returnErr: context.DeadlineExceeded}
	d := dispatch.NewDispatcher(pub, reg, nil)

	_, _, err := d.Dispatch(context.Background(), dispatch.TaskSpec{
		SkillID: "summarize",
		Prompt:  "Summarize.",
	}, "ctx-006")

	if err == nil {
		t.Fatal("expected publish error, got nil")
	}
}
