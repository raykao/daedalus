package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/telemetry"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the OTel instrumentation library name for collector spans.
const tracerName = "github.com/raykao/daedalus/internal/orchestrator"

// ResultCollector subscribes to NATS result and status subjects and provides
// synchronous waiting for individual task outcomes.
type ResultCollector struct {
	conn    *nats.Conn
	logger  *slog.Logger
	mu      sync.Mutex
	results map[string]*TaskResult        // taskID -> result
	waiters map[string][]chan *TaskResult  // taskID -> waiting channels
}

// NewResultCollector creates a ResultCollector backed by the given NATS connection.
func NewResultCollector(conn *nats.Conn, logger *slog.Logger) *ResultCollector {
	if logger == nil {
		logger = slog.Default()
	}
	return &ResultCollector{
		conn:    conn,
		logger:  logger,
		results: make(map[string]*TaskResult),
		waiters: make(map[string][]chan *TaskResult),
	}
}

// Start subscribes to agent.results.> and agent.status.> subjects.
// It runs until the context is cancelled.
func (rc *ResultCollector) Start(ctx context.Context) error {
	resultSub, err := rc.conn.Subscribe("agent.results.>", func(msg *nats.Msg) {
		rc.handleResult(msg)
	})
	if err != nil {
		return fmt.Errorf("collector: subscribe agent.results.>: %w", err)
	}

	statusSub, err := rc.conn.Subscribe("agent.status.>", func(msg *nats.Msg) {
		rc.handleStatus(msg)
	})
	if err != nil {
		resultSub.Unsubscribe() //nolint:errcheck
		return fmt.Errorf("collector: subscribe agent.status.>: %w", err)
	}

	rc.logger.Info("collector: started, listening for results and status")

	<-ctx.Done()

	rc.logger.Info("collector: context cancelled, stopping")
	resultSub.Unsubscribe() //nolint:errcheck
	statusSub.Unsubscribe() //nolint:errcheck

	return nil
}

// WaitFor blocks until the result for the given taskID arrives or ctx is cancelled.
func (rc *ResultCollector) WaitFor(ctx context.Context, taskID string) (*TaskResult, error) {
	// Fast path: result already available.
	rc.mu.Lock()
	if r, ok := rc.results[taskID]; ok {
		rc.mu.Unlock()
		return r, nil
	}

	// Register a waiter channel.
	ch := make(chan *TaskResult, 1)
	rc.waiters[taskID] = append(rc.waiters[taskID], ch)
	rc.mu.Unlock()

	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		// Clean up the waiter channel.
		rc.mu.Lock()
		waiters := rc.waiters[taskID]
		updated := make([]chan *TaskResult, 0, len(waiters))
		for _, w := range waiters {
			if w != ch {
				updated = append(updated, w)
			}
		}
		if len(updated) == 0 {
			delete(rc.waiters, taskID)
		} else {
			rc.waiters[taskID] = updated
		}
		rc.mu.Unlock()
		return nil, fmt.Errorf("collector: wait for task %s: %w", taskID, ctx.Err())
	}
}

// WaitForAll blocks until results for all given taskIDs arrive or ctx is cancelled.
// Returns a map of taskID -> TaskResult. On context cancellation, returns partial
// results alongside the context error.
func (rc *ResultCollector) WaitForAll(ctx context.Context, taskIDs []string) (map[string]*TaskResult, error) {
	type pair struct {
		id     string
		result *TaskResult
		err    error
	}

	ch := make(chan pair, len(taskIDs))

	for _, id := range taskIDs {
		go func(taskID string) {
			r, err := rc.WaitFor(ctx, taskID)
			ch <- pair{id: taskID, result: r, err: err}
		}(id)
	}

	out := make(map[string]*TaskResult, len(taskIDs))
	var firstErr error

	for range taskIDs {
		p := <-ch
		if p.err != nil {
			if firstErr == nil {
				firstErr = p.err
			}
		} else {
			out[p.id] = p.result
		}
	}

	return out, firstErr
}

// applyMetadataTraceFallback reattaches a trace_id taken from the message
// envelope (Task.Metadata["trace_id"]) as a remote parent on ctx, but only if
// the incoming ctx does not already carry a valid SpanContext from NATS
// headers. NATS headers (W3C traceparent) win when present because they carry
// both trace_id and the publisher's span_id; metadata-only carries trace_id
// alone and produces a remote root span within the existing trace.
func applyMetadataTraceFallback(ctx context.Context, traceIDFromMetadata string) context.Context {
	if traceIDFromMetadata == "" {
		return ctx
	}
	if trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	tid, err := trace.TraceIDFromHex(traceIDFromMetadata)
	if err != nil {
		return ctx
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}

// handleResult processes an incoming message on agent.results.<taskID>.
func (rc *ResultCollector) handleResult(msg *nats.Msg) {
	taskID := extractSuffix(msg.Subject, "agent.results.")
	if taskID == "" {
		rc.logger.Warn("collector: could not extract taskID from subject", "subject", msg.Subject)
		return
	}

	// Reattach the trace context from the publisher's NATS headers. If
	// headers are absent (e.g. legacy publishers), fall back to the
	// trace_id stamped in Task.Metadata. We then emit a
	// "collector.receive.result" span that re-joins the original trace.
	ctx := telemetry.ExtractNATSHeaders(context.Background(), msg.Header)

	var task a2a.Task
	if err := json.Unmarshal(msg.Data, &task); err != nil {
		rc.logger.Error("collector: unmarshal result failed",
			"subject", msg.Subject,
			"taskID", taskID,
			"err", err,
		)
		return
	}

	// Prefer the task ID embedded in the message over the subject suffix.
	if task.ID != "" {
		taskID = task.ID
	}

	traceIDFromMetadata, _ := task.Metadata["trace_id"].(string)
	ctx = applyMetadataTraceFallback(ctx, traceIDFromMetadata)
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "collector.receive.result",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("daedalus.task.id", taskID),
			attribute.String("daedalus.task.state", string(task.Status.State)),
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", msg.Subject),
			attribute.String("daedalus.task.metadata.trace_id", traceIDFromMetadata),
		),
	)
	defer span.End()
	_ = ctx

	tr := &TaskResult{
		TaskID: taskID,
		Status: task.Status.State,
		Task:   &task,
	}

	rc.logger.Info("collector: result received",
		"taskID", taskID,
		"state", task.Status.State,
	)

	rc.storeAndNotify(taskID, tr)
}

// handleStatus processes an incoming message on agent.status.<taskID>.
// Intermediate status updates are logged but do not unblock WaitFor callers
// unless the task has reached a terminal state.
func (rc *ResultCollector) handleStatus(msg *nats.Msg) {
	taskID := extractSuffix(msg.Subject, "agent.status.")
	if taskID == "" {
		return
	}

	// Reattach trace context from NATS headers so the status span joins the
	// publisher's trace.
	ctx := telemetry.ExtractNATSHeaders(context.Background(), msg.Header)

	var status a2a.TaskStatus
	if err := json.Unmarshal(msg.Data, &status); err != nil {
		rc.logger.Debug("collector: unmarshal status failed",
			"subject", msg.Subject,
			"err", err,
		)
		return
	}

	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "collector.receive.status",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("daedalus.task.id", taskID),
			attribute.String("daedalus.task.state", string(status.State)),
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", msg.Subject),
		),
	)
	defer span.End()
	_ = ctx

	rc.logger.Info("collector: status update",
		"taskID", taskID,
		"state", status.State,
	)

	// If the status is terminal and we don't yet have a result, create a partial
	// TaskResult so WaitFor callers are not blocked indefinitely.
	if isTerminal(status.State) {
		rc.mu.Lock()
		_, alreadyDone := rc.results[taskID]
		rc.mu.Unlock()

		if !alreadyDone {
			tr := &TaskResult{
				TaskID: taskID,
				Status: status.State,
			}
			rc.storeAndNotify(taskID, tr)
		}
	}
}

// storeAndNotify stores the result and notifies any registered waiters.
func (rc *ResultCollector) storeAndNotify(taskID string, tr *TaskResult) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Store (first write wins - a result message always supersedes a status).
	if existing, ok := rc.results[taskID]; !ok || existing.Task == nil {
		rc.results[taskID] = tr
	}

	// Notify all waiters.
	for _, ch := range rc.waiters[taskID] {
		select {
		case ch <- tr:
		default:
		}
	}
	delete(rc.waiters, taskID)
}

// extractSuffix strips the given prefix from a NATS subject and returns the
// remaining suffix (the task ID).
func extractSuffix(subject, prefix string) string {
	return strings.TrimPrefix(subject, prefix)
}

// isTerminal returns true if the task state represents a final outcome.
func isTerminal(state a2a.TaskState) bool {
	switch state {
	case a2a.TaskStateCompleted, a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected:
		return true
	}
	return false
}
