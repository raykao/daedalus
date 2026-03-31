package scheduler_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/raykao/agent-forge/internal/dispatch"
	"github.com/raykao/agent-forge/internal/orchestrator"
	"github.com/raykao/agent-forge/internal/scheduler"
)

// ---------------------------------------------------------------------------
// mockDispatcher - satisfies dispatch.TaskDispatcher
// ---------------------------------------------------------------------------

type dispatchCall struct {
	spec      dispatch.TaskSpec
	contextID string
}

type mockDispatcher struct {
	mu      sync.Mutex
	calls   []dispatchCall
	failIDs map[string]error // specID -> error to return from Dispatch
	delay   time.Duration
}

func newMockDispatcher() *mockDispatcher {
	return &mockDispatcher{failIDs: make(map[string]error)}
}

func (m *mockDispatcher) Dispatch(
	ctx context.Context, spec dispatch.TaskSpec, contextID string,
) (string, string, error) {
	m.mu.Lock()
	m.calls = append(m.calls, dispatchCall{spec: spec, contextID: contextID})
	m.mu.Unlock()

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}

	if err, ok := m.failIDs[spec.ID]; ok {
		return "", "", err
	}
	// Return spec.ID as the task ID (scheduler always sets spec.ID).
	return spec.ID, "agent.tasks." + spec.ID, nil
}

func (m *mockDispatcher) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockDispatcher) dispatched() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, len(m.calls))
	for i, c := range m.calls {
		ids[i] = c.spec.ID
	}
	return ids
}

// ---------------------------------------------------------------------------
// mockResultWaiter - satisfies scheduler.ResultWaiter
// ---------------------------------------------------------------------------

type mockResultWaiter struct {
	mu      sync.Mutex
	results map[string]*orchestrator.TaskResult
	waiters map[string][]chan *orchestrator.TaskResult
}

func newMockResultWaiter() *mockResultWaiter {
	return &mockResultWaiter{
		results: make(map[string]*orchestrator.TaskResult),
		waiters: make(map[string][]chan *orchestrator.TaskResult),
	}
}

func (m *mockResultWaiter) WaitFor(ctx context.Context, taskID string) (*orchestrator.TaskResult, error) {
	m.mu.Lock()
	if r, ok := m.results[taskID]; ok {
		m.mu.Unlock()
		return r, nil
	}
	ch := make(chan *orchestrator.TaskResult, 1)
	m.waiters[taskID] = append(m.waiters[taskID], ch)
	m.mu.Unlock()

	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// completeTask pre-populates or signals a task result.
func (m *mockResultWaiter) completeTask(taskID string, state a2a.TaskState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := &orchestrator.TaskResult{TaskID: taskID, Status: state}
	m.results[taskID] = r
	for _, ch := range m.waiters[taskID] {
		select {
		case ch <- r:
		default:
		}
	}
	delete(m.waiters, taskID)
}

// prePopulate adds completed results for multiple task IDs at once.
func (m *mockResultWaiter) prePopulate(ids ...string) {
	for _, id := range ids {
		m.completeTask(id, a2a.TaskStateCompleted)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// makeTask creates a minimal ScheduledTask.
func makeTask(id string, deps ...string) scheduler.ScheduledTask {
	return scheduler.ScheduledTask{
		TaskSpec:  dispatch.TaskSpec{ID: id, SkillID: "test-skill", Prompt: "do " + id},
		DependsOn: deps,
	}
}

// sortedBatch returns a sorted copy of a batch for order-independent comparison.
func sortedBatch(b []string) []string {
	c := append([]string{}, b...)
	sort.Strings(c)
	return c
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestScheduler_LinearChain(t *testing.T) {
	// A -> B -> C: one task per batch.
	waiter := newMockResultWaiter()
	waiter.prePopulate("A", "B", "C")
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		ContextID: "ctx-linear",
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
			makeTask("C", "B"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(result.ExecutionOrder) != 3 {
		t.Fatalf("expected 3 batches, got %d: %v", len(result.ExecutionOrder), result.ExecutionOrder)
	}
	// Each batch must have exactly one task.
	for i, batch := range result.ExecutionOrder {
		if len(batch) != 1 {
			t.Errorf("batch %d: expected 1 task, got %d: %v", i, len(batch), batch)
		}
	}
	// Verify task IDs in order.
	if result.ExecutionOrder[0][0] != "A" {
		t.Errorf("batch 0 should be A, got %s", result.ExecutionOrder[0][0])
	}
	if result.ExecutionOrder[1][0] != "B" {
		t.Errorf("batch 1 should be B, got %s", result.ExecutionOrder[1][0])
	}
	if result.ExecutionOrder[2][0] != "C" {
		t.Errorf("batch 2 should be C, got %s", result.ExecutionOrder[2][0])
	}
	// All statuses completed.
	for _, id := range []string{"A", "B", "C"} {
		if result.TaskResults[id].Status != "completed" {
			t.Errorf("task %s: expected completed, got %s", id, result.TaskResults[id].Status)
		}
	}
}

func TestScheduler_Diamond(t *testing.T) {
	// A -> {B, C} -> D
	waiter := newMockResultWaiter()
	waiter.prePopulate("A", "B", "C", "D")
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		ContextID: "ctx-diamond",
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
			makeTask("C", "A"),
			makeTask("D", "B", "C"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(result.ExecutionOrder) != 3 {
		t.Fatalf("expected 3 batches, got %d: %v", len(result.ExecutionOrder), result.ExecutionOrder)
	}

	// Batch 0: A only.
	if sortedBatch(result.ExecutionOrder[0])[0] != "A" || len(result.ExecutionOrder[0]) != 1 {
		t.Errorf("batch 0 should be [A], got %v", result.ExecutionOrder[0])
	}
	// Batch 1: B and C in parallel.
	b1 := sortedBatch(result.ExecutionOrder[1])
	if len(b1) != 2 || b1[0] != "B" || b1[1] != "C" {
		t.Errorf("batch 1 should be [B C], got %v", b1)
	}
	// Batch 2: D.
	if sortedBatch(result.ExecutionOrder[2])[0] != "D" || len(result.ExecutionOrder[2]) != 1 {
		t.Errorf("batch 2 should be [D], got %v", result.ExecutionOrder[2])
	}
	// Verify batch numbers in outcomes.
	if result.TaskResults["A"].Batch != 0 {
		t.Errorf("A batch: expected 0, got %d", result.TaskResults["A"].Batch)
	}
	if result.TaskResults["D"].Batch != 2 {
		t.Errorf("D batch: expected 2, got %d", result.TaskResults["D"].Batch)
	}
}

func TestScheduler_IndependentTasks(t *testing.T) {
	// A, B, C with no deps: all in batch 0.
	waiter := newMockResultWaiter()
	waiter.prePopulate("A", "B", "C")
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		ContextID: "ctx-independent",
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B"),
			makeTask("C"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(result.ExecutionOrder) != 1 {
		t.Fatalf("expected 1 batch, got %d: %v", len(result.ExecutionOrder), result.ExecutionOrder)
	}
	b0 := sortedBatch(result.ExecutionOrder[0])
	if len(b0) != 3 || b0[0] != "A" || b0[1] != "B" || b0[2] != "C" {
		t.Errorf("batch 0 should be [A B C], got %v", b0)
	}
	if disp.callCount() != 3 {
		t.Errorf("expected 3 dispatch calls, got %d", disp.callCount())
	}
}

func TestScheduler_FailedTaskCascades(t *testing.T) {
	// A -> B: A fails, B must be skipped.
	waiter := newMockResultWaiter()
	waiter.completeTask("A", a2a.TaskStateFailed) // A returns failed
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		ContextID: "ctx-cascade",
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if result.TaskResults["A"].Status != "failed" {
		t.Errorf("A: expected failed, got %s", result.TaskResults["A"].Status)
	}
	if result.TaskResults["B"].Status != "skipped" {
		t.Errorf("B: expected skipped, got %s", result.TaskResults["B"].Status)
	}
	// B must NOT have been dispatched.
	for _, id := range disp.dispatched() {
		if id == "B" {
			t.Error("B should not have been dispatched after A failed")
		}
	}
}

func TestScheduler_FailedTaskCascades_Transitive(t *testing.T) {
	// A -> B -> C -> D: A fails, B, C, D all skipped.
	waiter := newMockResultWaiter()
	waiter.completeTask("A", a2a.TaskStateFailed)
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
			makeTask("C", "B"),
			makeTask("D", "C"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if result.TaskResults["A"].Status != "failed" {
		t.Errorf("A: expected failed, got %s", result.TaskResults["A"].Status)
	}
	for _, id := range []string{"B", "C", "D"} {
		if result.TaskResults[id].Status != "skipped" {
			t.Errorf("%s: expected skipped, got %s", id, result.TaskResults[id].Status)
		}
	}
	if disp.callCount() != 1 {
		t.Errorf("expected only 1 dispatch (A), got %d: %v", disp.callCount(), disp.dispatched())
	}
}

func TestScheduler_MixedDeps(t *testing.T) {
	// A -> B, C -> D (independent branches), E has no deps.
	// A and C and E are all independent and run in batch 0.
	// B and D run when their predecessors complete.
	waiter := newMockResultWaiter()
	waiter.prePopulate("A", "B", "C", "D", "E")
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
			makeTask("C"),
			makeTask("D", "C"),
			makeTask("E"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}

	// Batch 0 must contain A, C, E.
	b0 := sortedBatch(result.ExecutionOrder[0])
	if len(b0) != 3 || b0[0] != "A" || b0[1] != "C" || b0[2] != "E" {
		t.Errorf("batch 0: expected [A C E], got %v", b0)
	}
	// Batch 1 must contain B and D.
	if len(result.ExecutionOrder) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(result.ExecutionOrder))
	}
	b1 := sortedBatch(result.ExecutionOrder[1])
	if len(b1) != 2 || b1[0] != "B" || b1[1] != "D" {
		t.Errorf("batch 1: expected [B D], got %v", b1)
	}
	// All completed.
	for _, id := range []string{"A", "B", "C", "D", "E"} {
		if result.TaskResults[id].Status != "completed" {
			t.Errorf("%s: expected completed, got %s", id, result.TaskResults[id].Status)
		}
	}
}

func TestScheduler_FailedBranch_IndependentBranchContinues(t *testing.T) {
	// A -> B (A fails, B skipped), but C -> D (independent, both succeed).
	waiter := newMockResultWaiter()
	waiter.completeTask("A", a2a.TaskStateFailed)
	waiter.prePopulate("C", "D")
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
			makeTask("C"),
			makeTask("D", "C"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}

	if result.TaskResults["A"].Status != "failed" {
		t.Errorf("A: expected failed, got %s", result.TaskResults["A"].Status)
	}
	if result.TaskResults["B"].Status != "skipped" {
		t.Errorf("B: expected skipped, got %s", result.TaskResults["B"].Status)
	}
	if result.TaskResults["C"].Status != "completed" {
		t.Errorf("C: expected completed, got %s", result.TaskResults["C"].Status)
	}
	if result.TaskResults["D"].Status != "completed" {
		t.Errorf("D: expected completed, got %s", result.TaskResults["D"].Status)
	}
}

func TestScheduler_DispatchError_CascadesDownstream(t *testing.T) {
	dispErr := errors.New("no agent available")
	disp := newMockDispatcher()
	disp.failIDs["A"] = dispErr
	waiter := newMockResultWaiter()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
		},
	}

	result, err := sched.Schedule(context.Background(), req)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if result.TaskResults["A"].Status != "failed" {
		t.Errorf("A: expected failed, got %s", result.TaskResults["A"].Status)
	}
	if result.TaskResults["B"].Status != "skipped" {
		t.Errorf("B: expected skipped, got %s", result.TaskResults["B"].Status)
	}
}

func TestScheduler_ContextCancellation(t *testing.T) {
	// Dispatcher hangs until context is cancelled.
	disp := &mockDispatcher{
		failIDs: make(map[string]error),
		delay:   10 * time.Second, // very long - will be cancelled
	}
	waiter := newMockResultWaiter()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	result, err := sched.Schedule(ctx, req)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if result == nil {
		t.Fatal("expected partial result alongside error, got nil")
	}
	// At minimum, all tasks should have some outcome recorded.
	for _, id := range []string{"A", "B"} {
		if result.TaskResults[id] == nil {
			t.Errorf("missing outcome for task %s", id)
		}
	}
}

func TestScheduler_CycleRejection(t *testing.T) {
	disp := newMockDispatcher()
	waiter := newMockResultWaiter()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A", "B"),
			makeTask("B", "A"),
		},
	}

	_, err := sched.Schedule(context.Background(), req)
	if err == nil {
		t.Fatal("expected cycle validation error, got nil")
	}
	// Nothing should have been dispatched.
	if disp.callCount() != 0 {
		t.Errorf("expected 0 dispatch calls before validation, got %d", disp.callCount())
	}
}

func TestScheduler_EmptyRequest(t *testing.T) {
	sched := scheduler.NewScheduler(newMockDispatcher(), newMockResultWaiter(), nil)
	_, err := sched.Schedule(context.Background(), scheduler.ScheduleRequest{})
	if err == nil {
		t.Fatal("expected error for empty ScheduleRequest, got nil")
	}
}

func TestScheduler_UnknownDependency(t *testing.T) {
	sched := scheduler.NewScheduler(newMockDispatcher(), newMockResultWaiter(), nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A", "nonexistent"),
		},
	}
	_, err := sched.Schedule(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unknown dependency, got nil")
	}
}

func TestScheduler_WaitForContextCancellation_PartialResults(t *testing.T) {
	// A completes, but waiter for B never resolves (context is cancelled).
	waiter := newMockResultWaiter()
	waiter.completeTask("A", a2a.TaskStateCompleted)
	// B will block on WaitFor until context is cancelled.
	disp := newMockDispatcher()

	sched := scheduler.NewScheduler(disp, waiter, nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("B", "A"),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := sched.Schedule(ctx, req)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
	if result == nil {
		t.Fatal("expected partial result, got nil")
	}
	if result.TaskResults["A"].Status != "completed" {
		t.Errorf("A: expected completed, got %s", result.TaskResults["A"].Status)
	}
	// B should be canceled (context error), not failed.
	if result.TaskResults["B"] == nil {
		t.Fatal("expected B in results even on cancellation")
	}
	if result.TaskResults["B"].Status != "canceled" {
		t.Errorf("B: expected canceled, got %s", result.TaskResults["B"].Status)
	}
}

func TestScheduler_DuplicateTaskIDsReturnsError(t *testing.T) {
	sched := scheduler.NewScheduler(newMockDispatcher(), newMockResultWaiter(), nil)
	req := scheduler.ScheduleRequest{
		Tasks: []scheduler.ScheduledTask{
			makeTask("A"),
			makeTask("A"), // duplicate
		},
	}
	_, err := sched.Schedule(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for duplicate task ID, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate task ID") {
		t.Errorf("expected error to contain %q, got: %v", "duplicate task ID", err)
	}
}

// containsSubstring reports whether s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
