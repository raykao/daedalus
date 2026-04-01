// Package orchestrator coordinates fan-out dispatch of multiple tasks to
// agent workers via NATS.
package orchestrator

import (
	"errors"
	"fmt"
	"time"

	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/dispatch"
)

// FanOutRequest groups multiple tasks for parallel dispatch.
type FanOutRequest struct {
	Tasks     []dispatch.TaskSpec // Tasks to dispatch
	ContextID string              // Shared context ID linking all tasks
	Metadata  map[string]any      // Request-level metadata
}

// FanOutResult aggregates results from all dispatched tasks.
type FanOutResult struct {
	ContextID   string
	Tasks       []TaskResult
	StartedAt   time.Time
	CompletedAt time.Time
}

// TaskResult holds the outcome of a single dispatched task.
type TaskResult struct {
	TaskID       string
	SkillID      string
	Status       a2a.TaskState // completed, failed, canceled
	Task         *a2a.Task     // Full A2A Task response (nil if failed before dispatch)
	Error        error         // Non-nil if dispatch or execution failed
	DispatchedAt time.Time
	CompletedAt  time.Time
}

// Validate checks that the FanOutRequest is well-formed.
// Returns an error if the request has no tasks, or if any task is missing
// a SkillID or Prompt.
func (r FanOutRequest) Validate() error {
	if len(r.Tasks) == 0 {
		return errors.New("orchestrator: FanOutRequest must have at least one task")
	}
	for i, t := range r.Tasks {
		if t.SkillID == "" {
			return fmt.Errorf("orchestrator: task[%d]: SkillID is required", i)
		}
		if t.Prompt == "" {
			return fmt.Errorf("orchestrator: task[%d]: Prompt is required", i)
		}
	}
	return nil
}
