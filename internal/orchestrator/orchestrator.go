// Package orchestrator coordinates fan-out dispatch of multiple tasks to
// agent workers via NATS.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/raykao/agent-forge/internal/dispatch"
)

// Orchestrator coordinates fan-out dispatch of multiple tasks concurrently.
type Orchestrator struct {
	dispatcher dispatch.TaskDispatcher
	logger     *slog.Logger
}

// New creates an Orchestrator with the given dispatcher and logger.
func New(dispatcher dispatch.TaskDispatcher, logger *slog.Logger) *Orchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		dispatcher: dispatcher,
		logger:     logger,
	}
}

// FanOut dispatches all tasks in the request concurrently and collects results.
// Tasks are dispatched in parallel using goroutines.
// The function blocks until all tasks have been published to NATS, fail, or the
// context is cancelled.
//
// FanOut dispatches tasks but does NOT wait for worker completion. Result
// collection (subscribing to agent.results.<taskId>) is handled by ResultCollector.
func (o *Orchestrator) FanOut(ctx context.Context, req FanOutRequest) (*FanOutResult, error) {
	// 1. Validate request.
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// 2. Generate ContextID if empty.
	if req.ContextID == "" {
		id, err := generateContextID()
		if err != nil {
			return nil, fmt.Errorf("orchestrator: generate context ID: %w", err)
		}
		req.ContextID = id
	}

	result := &FanOutResult{
		ContextID: req.ContextID,
		Tasks:     make([]TaskResult, len(req.Tasks)),
		StartedAt: time.Now().UTC(),
	}

	o.logger.Info("orchestrator: fan-out started",
		"contextID", req.ContextID,
		"taskCount", len(req.Tasks),
	)

	// 3. Launch goroutines for each task dispatch.
	var wg sync.WaitGroup
	resultsCh := make(chan indexedResult, len(req.Tasks))

	for i, spec := range req.Tasks {
		wg.Add(1)
		go func(idx int, s dispatch.TaskSpec) {
			defer wg.Done()

			tr := TaskResult{
				SkillID:      s.SkillID,
				DispatchedAt: time.Now().UTC(),
			}

			taskID, _, err := o.dispatcher.Dispatch(ctx, s, req.ContextID)
			tr.CompletedAt = time.Now().UTC()

			if err != nil {
				tr.TaskID = s.ID // may be empty; that is fine
				tr.Error = err
				tr.Status = a2a.TaskStateFailed
				o.logger.Error("orchestrator: dispatch failed",
					"contextID", req.ContextID,
					"skillID", s.SkillID,
					"err", err,
				)
			} else {
				tr.TaskID = taskID
				tr.Status = a2a.TaskStateSubmitted
				o.logger.Info("orchestrator: task dispatched",
					"contextID", req.ContextID,
					"taskID", taskID,
					"skillID", s.SkillID,
				)
			}

			resultsCh <- indexedResult{idx: idx, result: tr}
		}(i, spec)
	}

	// 4. Wait in a separate goroutine so we can respect context cancellation.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines finished normally.
	case <-ctx.Done():
		// Context cancelled - collect whatever arrived so far.
		o.logger.Warn("orchestrator: context cancelled during fan-out",
			"contextID", req.ContextID,
		)
		// Wait for the goroutines to notice ctx cancellation and finish.
		<-done
	}

	close(resultsCh)

	// 5. Collect results.
	for r := range resultsCh {
		result.Tasks[r.idx] = r.result
	}

	result.CompletedAt = time.Now().UTC()

	if ctx.Err() != nil {
		return result, fmt.Errorf("orchestrator: fan-out interrupted: %w", ctx.Err())
	}

	return result, nil
}

// indexedResult pairs a goroutine index with its TaskResult so results can
// be placed in the correct slot of the output slice.
type indexedResult struct {
	idx    int
	result TaskResult
}

// generateContextID produces a random 12-byte hex string for use as a context ID.
func generateContextID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "ctx-" + hex.EncodeToString(b), nil
}
