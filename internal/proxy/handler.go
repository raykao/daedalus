package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/raykao/agent-forge/internal/acp"
	"github.com/raykao/agent-forge/internal/queue"
)

// sessionCancelTimeout is the per-session timeout for ACP cancel calls during shutdown.
const sessionCancelTimeout = 5 * time.Second

// Handler orchestrates the dequeue -> ACP -> publish flow
type Handler struct {
	acpClient *acp.Client
	publisher *queue.Publisher
	workDir   string
	logger    *slog.Logger

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

// NewHandler creates a new proxy handler
func NewHandler(acpClient *acp.Client, publisher *queue.Publisher, workDir string, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		acpClient: acpClient,
		publisher: publisher,
		workDir:   workDir,
		logger:    logger,
		sessions:  make(map[string]struct{}),
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

	// Create a new ACP session
	sessionID, err := h.acpClient.NewSession(ctx, h.workDir)
	if err != nil {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: acp session/new: %w", err))
	}
	h.registerSession(sessionID)
	defer h.deregisterSession(sessionID)
	h.logger.Info("proxy: acp session created", "sessionId", sessionID, "taskId", taskID)

	// Extract prompt from message parts
	prompt := req.Message.ExtractPromptText()
	if prompt == "" {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: no text content in message parts"))
	}

	// Send prompt and collect response
	content, err := h.acpClient.Prompt(ctx, sessionID, prompt)
	if err != nil {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: acp session/prompt: %w", err))
	}

	h.logger.Info("proxy: prompt completed",
		"taskId", taskID,
		"sessionId", sessionID,
		"contentLength", len(content),
	)

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

	// Publish result
	if h.publisher != nil {
		resultSubject := "agent.results." + taskID
		if err := h.publisher.PublishJSON(ctx, resultSubject, task); err != nil {
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
