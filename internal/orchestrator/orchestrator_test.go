package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/raykao/agent-forge/internal/dispatch"
	"github.com/raykao/agent-forge/internal/orchestrator"
)

// ---------------------------------------------------------------------------
// mockDispatcher - controls Dispatch behaviour in tests.
// ---------------------------------------------------------------------------

type mockDispatchEntry struct {
	taskID  string
	subject string
	err     error
	delay   time.Duration
}

type mockDispatcher struct {
	mu      sync.Mutex
	calls   []dispatchCall
	entries []mockDispatchEntry // index matches call order; last entry is reused
	idx     int
}

type dispatchCall struct {
	spec      dispatch.TaskSpec
	contextID string
}

func (m *mockDispatcher) next() mockDispatchEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx >= len(m.entries) {
		return mockDispatchEntry{taskID: "auto-id", subject: "agent.tasks.x"}
	}
	e := m.entries[m.idx]
	if m.idx < len(m.entries)-1 {
		m.idx++
	}
	return e
}

func (m *mockDispatcher) Dispatch(ctx context.Context, spec dispatch.TaskSpec, contextID string) (string, string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, dispatchCall{spec: spec, contextID: contextID})
	m.mu.Unlock()

	e := m.next()
	if e.delay > 0 {
		select {
		case <-time.After(e.delay):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	if e.err != nil {
		return "", "", e.err
	}
	id := e.taskID
	if id == "" {
		id = fmt.Sprintf("%s-task", contextID)
	}
	return id, e.subject, nil
}

func (m *mockDispatcher) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// ---------------------------------------------------------------------------
// funcDispatcher wraps an inline function as a TaskDispatcher.
// ---------------------------------------------------------------------------

type funcDispatcher struct {
	fn func(ctx context.Context, spec dispatch.TaskSpec, contextID string) (string, string, error)
}

func (f *funcDispatcher) Dispatch(ctx context.Context, spec dispatch.TaskSpec, contextID string) (string, string, error) {
	return f.fn(ctx, spec, contextID)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFanOut_DispatchesAllTasks(t *testing.T) {
	mock := &mockDispatcher{
		entries: []mockDispatchEntry{
			{taskID: "t1", subject: "s1"},
			{taskID: "t2", subject: "s2"},
			{taskID: "t3", subject: "s3"},
		},
	}

	o := orchestrator.New(mock, nil)
	req := orchestrator.FanOutRequest{
		ContextID: "ctx-multi",
		Tasks: []dispatch.TaskSpec{
			{SkillID: "summarize", Prompt: "Summarize A."},
			{SkillID: "translate", Prompt: "Translate B."},
			{SkillID: "classify", Prompt: "Classify C."},
		},
	}

	result, err := o.FanOut(context.Background(), req)
	if err != nil {
		t.Fatalf("FanOut returned error: %v", err)
	}
	if len(result.Tasks) != 3 {
		t.Fatalf("expected 3 task results, got %d", len(result.Tasks))
	}
	if mock.callCount() != 3 {
		t.Errorf("expected 3 Dispatch calls, got %d", mock.callCount())
	}
}

func TestFanOut_EmptyRequestReturnsValidationError(t *testing.T) {
	o := orchestrator.New(&funcDispatcher{fn: func(_ context.Context, _ dispatch.TaskSpec, _ string) (string, string, error) {
		return "", "", nil
	}}, nil)

	_, err := o.FanOut(context.Background(), orchestrator.FanOutRequest{})
	if err == nil {
		t.Fatal("expected validation error for empty FanOutRequest, got nil")
	}
}

func TestFanOut_GeneratesContextIDWhenEmpty(t *testing.T) {
	var capturedContextID string
	o := orchestrator.New(&funcDispatcher{fn: func(_ context.Context, _ dispatch.TaskSpec, contextID string) (string, string, error) {
		capturedContextID = contextID
		return "task-1", "subject-1", nil
	}}, nil)

	req := orchestrator.FanOutRequest{
		// ContextID intentionally left empty
		Tasks: []dispatch.TaskSpec{
			{SkillID: "summarize", Prompt: "Summarize."},
		},
	}

	_, err := o.FanOut(context.Background(), req)
	if err != nil {
		t.Fatalf("FanOut: %v", err)
	}
	if capturedContextID == "" {
		t.Error("expected generated contextID to be passed to Dispatch, got empty")
	}
}

func TestFanOut_ContextCancellationReturnsPartialResults(t *testing.T) {
	blockCh := make(chan struct{})

	callCount := 0
	var mu sync.Mutex

	o := orchestrator.New(&funcDispatcher{fn: func(ctx context.Context, _ dispatch.TaskSpec, _ string) (string, string, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		select {
		case <-blockCh:
			return "task-x", "subj-x", nil
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req := orchestrator.FanOutRequest{
		ContextID: "ctx-cancel",
		Tasks: []dispatch.TaskSpec{
			{SkillID: "summarize", Prompt: "Summarize."},
			{SkillID: "translate", Prompt: "Translate."},
		},
	}

	result, err := o.FanOut(ctx, req)
	if err == nil {
		t.Error("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
	// Partial results should still be returned.
	if result == nil {
		t.Fatal("expected partial results, got nil")
	}
	if len(result.Tasks) != 2 {
		t.Errorf("expected 2 task slots, got %d", len(result.Tasks))
	}
}

func TestFanOut_DispatchErrorRecordedInResult(t *testing.T) {
	dispatchErr := errors.New("no agent for skill")

	o := orchestrator.New(&funcDispatcher{fn: func(_ context.Context, _ dispatch.TaskSpec, _ string) (string, string, error) {
		return "", "", dispatchErr
	}}, nil)

	req := orchestrator.FanOutRequest{
		ContextID: "ctx-err",
		Tasks: []dispatch.TaskSpec{
			{SkillID: "nonexistent", Prompt: "Do something."},
		},
	}

	result, err := o.FanOut(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected top-level error (should be captured per-task): %v", err)
	}
	if result.Tasks[0].Error == nil {
		t.Error("expected per-task error, got nil")
	}
	if result.Tasks[0].Status != a2a.TaskStateFailed {
		t.Errorf("expected status %q, got %q", a2a.TaskStateFailed, result.Tasks[0].Status)
	}
}
