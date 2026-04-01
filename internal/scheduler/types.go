package scheduler

import "github.com/raykao/daedalus/internal/dispatch"

// ScheduledTask extends TaskSpec with dependency information.
type ScheduledTask struct {
	dispatch.TaskSpec
	DependsOn []string // IDs of tasks that must complete before this one
}

// ScheduleRequest groups tasks with their dependency graph for ordered execution.
type ScheduleRequest struct {
	Tasks     []ScheduledTask
	ContextID string
	Metadata  map[string]any
}

// ScheduleResult holds the outcome of scheduled execution.
type ScheduleResult struct {
	ContextID      string
	TaskResults    map[string]*TaskOutcome
	ExecutionOrder [][]string // batches of task IDs that ran together (dispatched)
}

// TaskOutcome wraps the dispatch result with scheduling metadata.
type TaskOutcome struct {
	TaskID string
	Status string // "completed", "failed", "canceled", "skipped"
	Error  error
	Batch  int // which batch this ran in (0-indexed)
}
