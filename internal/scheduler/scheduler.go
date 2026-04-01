package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/dispatch"
	"github.com/raykao/daedalus/internal/orchestrator"
)

// ResultWaiter decouples the Scheduler from the concrete ResultCollector.
// orchestrator.ResultCollector satisfies this interface.
type ResultWaiter interface {
	WaitFor(ctx context.Context, taskID string) (*orchestrator.TaskResult, error)
}

// Scheduler orchestrates DAG-ordered execution of ScheduledTasks.
// Tasks with no unmet dependencies run in parallel (a "wave" or "batch");
// dependent tasks wait until all predecessors complete.
type Scheduler struct {
	dispatcher dispatch.TaskDispatcher
	collector  ResultWaiter
	logger     *slog.Logger
}

// NewScheduler creates a Scheduler backed by the given dispatcher and collector.
func NewScheduler(dispatcher dispatch.TaskDispatcher, collector ResultWaiter, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		dispatcher: dispatcher,
		collector:  collector,
		logger:     logger,
	}
}

// Schedule executes the tasks in req in dependency order.
//
// Execution model:
//  1. Build and validate a DAG from the task dependency edges.
//  2. Repeatedly dispatch the current "ready" wave (nodes with in-degree 0),
//     wait for all results, then release dependents.
//  3. If a task fails its downstream dependents are marked "skipped".
//  4. Context cancellation stops new dispatches; already-in-flight tasks are
//     awaited and their outcomes recorded. Returns ctx.Err() in that case.
func (s *Scheduler) Schedule(ctx context.Context, req ScheduleRequest) (*ScheduleResult, error) {
	if len(req.Tasks) == 0 {
		return nil, errors.New("scheduler: ScheduleRequest must have at least one task")
	}

	// Index tasks by ID.
	taskByID := make(map[string]*ScheduledTask, len(req.Tasks))
	for i := range req.Tasks {
		t := &req.Tasks[i]
		if t.ID == "" {
			return nil, fmt.Errorf("scheduler: task at index %d has empty ID", i)
		}
		if _, exists := taskByID[t.ID]; exists {
			return nil, fmt.Errorf("scheduler: duplicate task ID %q at index %d", t.ID, i)
		}
		taskByID[t.ID] = t
	}

	// Build DAG.
	d := NewDAG()
	for id := range taskByID {
		d.AddNode(id)
	}
	for _, t := range req.Tasks {
		for _, dep := range t.DependsOn {
			if _, exists := taskByID[dep]; !exists {
				return nil, fmt.Errorf("scheduler: task %q depends on unknown task %q", t.ID, dep)
			}
			if err := d.AddEdge(dep, t.ID); err != nil {
				return nil, fmt.Errorf("scheduler: %w", err)
			}
		}
	}

	if err := d.Validate(); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}

	contextID := req.ContextID
	outcomes := make(map[string]*TaskOutcome, len(req.Tasks))
	var executionOrder [][]string
	skip := make(map[string]bool)
	batchNum := 0

	for d.Remaining() > 0 {
		ready := d.Ready()

		// Partition ready nodes into: immediately-skip vs dispatch.
		var toDispatch []string
		skippedThisRound := false
		for _, id := range ready {
			if skip[id] {
				outcomes[id] = &TaskOutcome{TaskID: id, Status: "skipped", Batch: batchNum}
				d.Complete(id)
				// Propagate skip lazily to direct successors.
				for _, succ := range d.Dependents(id) {
					skip[succ] = true
				}
				skippedThisRound = true
			} else {
				toDispatch = append(toDispatch, id)
			}
		}

		if len(toDispatch) == 0 {
			if skippedThisRound {
				// More nodes may have become ready after completing skipped ones.
				continue
			}
			// No ready tasks and no progress - validation should have caught this.
			break
		}

		// Stop dispatching new tasks if the context is already cancelled.
		if ctxErr := ctx.Err(); ctxErr != nil {
			break
		}

		s.logger.Info("scheduler: dispatching batch",
			"batch", batchNum,
			"count", len(toDispatch),
			"tasks", toDispatch,
		)

		executionOrder = append(executionOrder, append([]string{}, toDispatch...))

		// --- Phase 1: dispatch all tasks in this wave in parallel ---
		type dispResult struct {
			specID     string
			dispatchID string
			err        error
		}
		dispResults := make([]dispResult, len(toDispatch))
		var dispWg sync.WaitGroup
		for i, id := range toDispatch {
			dispWg.Add(1)
			go func(i int, id string) {
				defer dispWg.Done()
				t := taskByID[id]
				spec := t.TaskSpec
				spec.ID = id // ensure spec ID is the scheduled task ID
				dispID, _, err := s.dispatcher.Dispatch(ctx, spec, contextID)
				dispResults[i] = dispResult{specID: id, dispatchID: dispID, err: err}
			}(i, id)
		}
		dispWg.Wait()

		// --- Phase 2: wait for all successfully-dispatched tasks ---
		type waitResult struct {
			specID string
			result *orchestrator.TaskResult
			err    error
		}
		waitCh := make(chan waitResult, len(toDispatch))
		var waitWg sync.WaitGroup

		for i, id := range toDispatch {
			dr := dispResults[i]
			if dr.err != nil {
				// Dispatch failed - record immediately without waiting.
				outcomes[id] = &TaskOutcome{
					TaskID: id,
					Status: "failed",
					Error:  dr.err,
					Batch:  batchNum,
				}
				d.Complete(id)
				for _, succ := range d.Dependents(id) {
					skip[succ] = true
				}
				continue
			}

			waitWg.Add(1)
			go func(specID, dispID string) {
				defer waitWg.Done()
				result, err := s.collector.WaitFor(ctx, dispID)
				waitCh <- waitResult{specID: specID, result: result, err: err}
			}(id, dr.dispatchID)
		}

		go func() {
			waitWg.Wait()
			close(waitCh)
		}()

		// Collect and process wait results sequentially.
		for wr := range waitCh {
			if wr.err != nil {
				status := "failed"
				if errors.Is(wr.err, context.Canceled) || errors.Is(wr.err, context.DeadlineExceeded) {
					status = "canceled"
				}
				outcomes[wr.specID] = &TaskOutcome{
					TaskID: wr.specID,
					Status: status,
					Error:  wr.err,
					Batch:  batchNum,
				}
				for _, succ := range d.Dependents(wr.specID) {
					skip[succ] = true
				}
			} else {
				status := outcomeStatus(wr.result.Status)
				outcomes[wr.specID] = &TaskOutcome{
					TaskID: wr.specID,
					Status: status,
					Batch:  batchNum,
				}
				// Cascade skip to dependents on non-completed results.
				if status != "completed" {
					for _, succ := range d.Dependents(wr.specID) {
						skip[succ] = true
					}
				}
			}
			d.Complete(wr.specID)
		}

		batchNum++
	}

	// Mark any tasks that never got an outcome (e.g., context cancelled before
	// they were dispatched) as canceled.
	for id := range taskByID {
		if outcomes[id] == nil {
			outcomes[id] = &TaskOutcome{TaskID: id, Status: "canceled", Batch: -1}
		}
	}

	result := &ScheduleResult{
		ContextID:      contextID,
		TaskResults:    outcomes,
		ExecutionOrder: executionOrder,
	}
	return result, ctx.Err()
}

// outcomeStatus maps an a2a.TaskState to a TaskOutcome status string.
func outcomeStatus(state a2a.TaskState) string {
	switch state {
	case a2a.TaskStateCompleted:
		return "completed"
	case a2a.TaskStateFailed:
		return "failed"
	case a2a.TaskStateCanceled:
		return "canceled"
	default:
		return string(state)
	}
}
