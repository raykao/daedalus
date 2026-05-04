package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/acp"
	contextmgmt "github.com/raykao/daedalus/internal/contextmgmt"
	"github.com/raykao/daedalus/internal/queue"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// tracerName is the OTel instrumentation library name for proxy spans.
const tracerName = "github.com/raykao/daedalus/internal/proxy"

// sessionCancelTimeout is the per-session timeout for ACP cancel calls during shutdown.
const sessionCancelTimeout = 5 * time.Second

// Handler orchestrates the dequeue -> ACP -> publish flow
type Handler struct {
	acpClient      *acp.Client
	publisher      *queue.Publisher
	workDir        string
	logger         *slog.Logger
	contextTracker *contextmgmt.Tracker

	// shuttingDown is set to true before Shutdown waits on the WaitGroup.
	// Handle checks this flag before calling wg.Add to avoid a panic on
	// WaitGroup reuse after Wait returns (per sync.WaitGroup documentation).
	shuttingDown atomic.Bool

	// initOnce ensures Initialize is called at most once. The first Handle
	// call's context is used for the RPC; subsequent calls use the stored error.
	// This is acceptable since Initialize is a one-time protocol handshake.
	initOnce sync.Once
	initErr  error

	// mu protects the sessions map.
	mu       sync.Mutex
	sessions map[string]struct{} // active ACP session IDs

	// wg tracks in-flight Handle calls so ShutdownManager can wait for them.
	wg sync.WaitGroup
}

// NewHandler creates a new proxy handler. The tracker parameter is optional;
// if nil, context usage tracking is disabled.
func NewHandler(acpClient *acp.Client, publisher *queue.Publisher, workDir string, logger *slog.Logger, tracker *contextmgmt.Tracker) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		acpClient:      acpClient,
		publisher:      publisher,
		workDir:        workDir,
		logger:         logger,
		contextTracker: tracker,
		sessions:       make(map[string]struct{}),
	}
}

// SetInitialized marks the handler as already initialized so that Handle does
// not call Initialize again. Call this after a successful Initialize in main.
func (h *Handler) SetInitialized() {
	h.initOnce.Do(func() {}) // consume the Once with no error
}

// Handle processes a single A2A SendMessageRequest from the queue.
// It registers itself with the WaitGroup for graceful shutdown tracking.
func (h *Handler) Handle(ctx context.Context, data []byte) error {
	// Atomically check shutdown flag and register with WaitGroup.
	// The mutex prevents a race where wg.Add(1) executes after wg.Wait()
	// returns in Shutdown, which would violate WaitGroup semantics.
	h.mu.Lock()
	if h.shuttingDown.Load() {
		h.mu.Unlock()
		return fmt.Errorf("proxy: rejecting message, shutdown in progress")
	}
	h.wg.Add(1)
	h.mu.Unlock()
	defer h.wg.Done()

	var req a2a.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("proxy: unmarshal SendMessageRequest: %w", err)
	}

	taskID := req.Message.TaskID
	if taskID == "" {
		taskID = req.Message.MessageID
	}

	// Start the per-task root span on the worker side. This sits as a child
	// of the nats.consume span set up by queue.Consumer.processMessage, so
	// the full trace chain back to the publisher is preserved.
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "proxy.handle",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			attribute.String("daedalus.task.id", taskID),
			attribute.String("daedalus.message.id", req.Message.MessageID),
			attribute.String("daedalus.context.id", req.Message.ContextID),
		),
	)
	defer span.End()

	// Also seed the ctx with any A2A-metadata-encoded trace context. If the
	// orchestrator chose to inject via metadata instead of NATS headers (or
	// in addition to), this stitches the two into one trace.
	if req.Message.Metadata != nil {
		// Note: we only Extract; we do NOT replace the active span. If the
		// metadata carries a different trace, it would manifest as a span
		// link rather than a parent change. Leaving as a future hook.
		_ = ctx
	}

	h.logger.Info("proxy: handling task",
		"taskId", taskID,
		"messageId", req.Message.MessageID,
		"role", req.Message.Role,
	)

	// Publish "working" status
	if err := h.publishStatus(ctx, taskID, a2a.TaskStateWorking, nil); err != nil {
		h.logger.Warn("proxy: failed to publish working status", "taskId", taskID, "err", err)
	}

	// Initialize ACP exactly once. The first Handle call's context is used for
	// the RPC; later calls observe the stored error. Safe for concurrent calls.
	h.initOnce.Do(func() {
		_, h.initErr = h.acpClient.Initialize(ctx)
	})
	if h.initErr != nil {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: acp initialize: %w", h.initErr))
	}

	// Create a new ACP session (instrumented).
	sessionID, err := h.acpSessionNew(ctx)
	if err != nil {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: acp session/new: %w", err))
	}
	h.registerSession(sessionID)
	defer func() {
		h.deregisterSession(sessionID)
		if h.contextTracker != nil {
			h.contextTracker.EndSession(sessionID)
		}
	}()
	h.logger.Info("proxy: acp session created", "sessionId", sessionID, "taskId", taskID)

	if h.contextTracker != nil {
		// TODO(phase3.3): Populate tokenLimit from model config once ACP
		// exposes the model's context window size in the initialize response.
		h.contextTracker.StartSession(sessionID, taskID, 0)
	}

	// Extract prompt from message parts
	prompt := req.Message.ExtractPromptText()
	if prompt == "" {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: no text content in message parts"))
	}

	// Send prompt and collect response (instrumented).
	content, err := h.acpPrompt(ctx, sessionID, prompt)
	if err != nil {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: acp session/prompt: %w", err))
	}

	h.logger.Info("proxy: prompt completed",
		"taskId", taskID,
		"sessionId", sessionID,
		"contentLength", len(content),
	)

	if h.contextTracker != nil {
		// Forward approximate token count based on content length.
		// Real token counting requires model-specific tokenizer; content length
		// is a reasonable proxy (1 token ~ 4 chars for English text).
		approxTokens := int64(len(prompt)+len(content)) / 4
		h.contextTracker.UpdateTokens(sessionID, approxTokens)
	}

	// Build A2A Task result
	now := time.Now().UTC().Format(time.RFC3339)
	artifact := a2a.Artifact{
		ArtifactID: taskID + "-response",
		Name:       "agent-response",
		Parts: []a2a.Part{
			{Text: content},
		},
	}
	resultMsg := &a2a.Message{
		MessageID: taskID + "-result",
		TaskID:    taskID,
		Role:      "agent",
		Parts:     []a2a.Part{{Text: content}},
	}
	task := a2a.Task{
		ID:        taskID,
		ContextID: req.Message.ContextID,
		Status: a2a.TaskStatus{
			State:     a2a.TaskStateCompleted,
			Message:   resultMsg,
			Timestamp: now,
		},
		Artifacts: []a2a.Artifact{artifact},
	}

	if h.contextTracker != nil {
		if metrics, ok := h.contextTracker.GetMetrics(sessionID); ok {
			if task.Metadata == nil {
				task.Metadata = make(map[string]any)
			}
			contextUsage := map[string]any{
				"currentTokens":   metrics.CurrentTokens,
				"turnCount":       metrics.TurnCount,
				"compactionCount": metrics.CompactionCount,
			}
			if metrics.TokenLimit > 0 {
				contextUsage["usagePercent"] = metrics.UsagePercent()
			}
			task.Metadata["contextUsage"] = contextUsage
		}
	}

	// Stamp trace_id into Task.Metadata for downstream consumers that
	// cannot read NATS headers (e.g. a future Mattermost client speaking
	// JSON-only). The full W3C traceparent still rides in NATS headers
	// via queue.Publisher; this is the belt-and-suspenders fallback.
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		if task.Metadata == nil {
			task.Metadata = make(map[string]any)
		}
		task.Metadata["trace_id"] = sc.TraceID().String()
	}

	// Publish result
	if h.publisher != nil {
		resultSubject := "agent.results." + taskID
		if err := h.publisher.PublishJSON(ctx, resultSubject, task); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return fmt.Errorf("proxy: publish result: %w", err)
		}
	}

	// Publish completed status
	if err := h.publishStatus(ctx, taskID, a2a.TaskStateCompleted, resultMsg); err != nil {
		h.logger.Warn("proxy: failed to publish completed status", "taskId", taskID, "err", err)
	}

	h.logger.Info("proxy: task completed", "taskId", taskID)
	return nil
}

// acpSessionNew wraps acp.Client.NewSession in an "acp.session.new" span.
func (h *Handler) acpSessionNew(ctx context.Context) (string, error) {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "acp.session.new",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "session/new"),
			attribute.String("daedalus.work_dir", h.workDir),
		),
	)
	defer span.End()
	sid, err := h.acpClient.NewSession(ctx, h.workDir)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return "", err
	}
	span.SetAttributes(attribute.String("daedalus.session.id", sid))
	return sid, nil
}

// acpPrompt wraps acp.Client.Prompt in an "acp.session.prompt" span. Per the
// audit, ACP session/update streaming notifications correlate to the in-flight
// prompt by sessionId, so they remain logically inside this span.
func (h *Handler) acpPrompt(ctx context.Context, sessionID, prompt string) (string, error) {
	tracer := otel.Tracer(tracerName)
	ctx, span := tracer.Start(ctx, "acp.session.prompt",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("rpc.system", "jsonrpc"),
			attribute.String("rpc.method", "session/prompt"),
			attribute.String("daedalus.session.id", sessionID),
			attribute.Int("daedalus.prompt.length", len(prompt)),
		),
	)
	defer span.End()
	content, err := h.acpClient.Prompt(ctx, sessionID, prompt)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return "", err
	}
	span.SetAttributes(attribute.Int("daedalus.response.length", len(content)))
	return content, nil
}

// registerSession records an active ACP session ID for shutdown tracking.
func (h *Handler) registerSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessions[sessionID] = struct{}{}
}

// deregisterSession removes a completed ACP session from tracking.
func (h *Handler) deregisterSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, sessionID)
}

// activeSessions returns a snapshot of currently active session IDs.
func (h *Handler) activeSessions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.sessions))
	for id := range h.sessions {
		ids = append(ids, id)
	}
	return ids
}

// CancelActiveSessions sends session/cancel for every tracked ACP session.
// Each cancel gets sessionCancelTimeout to avoid blocking shutdown indefinitely.
func (h *Handler) CancelActiveSessions(ctx context.Context) {
	sessions := h.activeSessions()
	if len(sessions) == 0 {
		return
	}
	h.logger.Info("proxy: cancelling active ACP sessions", "count", len(sessions))
	for _, sessionID := range sessions {
		cancelCtx, cancel := context.WithTimeout(ctx, sessionCancelTimeout)
		if err := h.acpClient.CancelSession(cancelCtx, sessionID); err != nil {
			h.logger.Warn("proxy: session cancel failed", "sessionId", sessionID, "err", err)
		}
		cancel()
	}
}

// fail publishes a failed status and returns the error
func (h *Handler) fail(ctx context.Context, taskID string, err error) error {
	h.logger.Error("proxy: task failed", "taskId", taskID, "err", err)

	failMsg := &a2a.Message{
		MessageID: taskID + "-error",
		TaskID:    taskID,
		Role:      "agent",
		Parts:     []a2a.Part{{Text: err.Error()}},
	}
	if pubErr := h.publishStatus(ctx, taskID, a2a.TaskStateFailed, failMsg); pubErr != nil {
		h.logger.Warn("proxy: failed to publish error status", "taskId", taskID, "err", pubErr)
	}
	return err
}

// publishStatus publishes a task status update to agent.status.<taskId>.
// A nil publisher is a no-op (used in tests without NATS).
func (h *Handler) publishStatus(ctx context.Context, taskID string, state a2a.TaskState, msg *a2a.Message) error {
	if h.publisher == nil {
		return nil
	}
	status := a2a.TaskStatus{
		State:     state,
		Message:   msg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	subject := "agent.status." + taskID
	return h.publisher.PublishJSON(ctx, subject, status)
}
