package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/raykao/agent-forge/internal/acp"
	"github.com/raykao/agent-forge/internal/queue"
)

// Handler orchestrates the dequeue → ACP → publish flow
type Handler struct {
	acpClient *acp.Client
	publisher *queue.Publisher
	workDir   string
	logger    *slog.Logger
	// Track initialized state
	initialized bool
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
	}
}

// Handle processes a single A2A SendMessageRequest from the queue
func (h *Handler) Handle(ctx context.Context, data []byte) error {
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

	// Initialize ACP once
	if !h.initialized {
		if _, err := h.acpClient.Initialize(ctx); err != nil {
			return h.fail(ctx, taskID, fmt.Errorf("proxy: acp initialize: %w", err))
		}
		h.initialized = true
	}

	// Create a new ACP session
	sessionID, err := h.acpClient.NewSession(ctx, h.workDir)
	if err != nil {
		return h.fail(ctx, taskID, fmt.Errorf("proxy: acp session/new: %w", err))
	}
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
	resultSubject := "agent.results." + taskID
	if err := h.publisher.PublishJSON(ctx, resultSubject, task); err != nil {
		return fmt.Errorf("proxy: publish result: %w", err)
	}

	// Publish completed status
	if err := h.publishStatus(ctx, taskID, a2a.TaskStateCompleted, resultMsg); err != nil {
		h.logger.Warn("proxy: failed to publish completed status", "taskId", taskID, "err", err)
	}

	h.logger.Info("proxy: task completed", "taskId", taskID, "resultSubject", resultSubject)
	return nil
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

// publishStatus publishes a task status update to agent.status.<taskId>
func (h *Handler) publishStatus(ctx context.Context, taskID string, state a2a.TaskState, msg *a2a.Message) error {
	status := a2a.TaskStatus{
		State:     state,
		Message:   msg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	subject := "agent.status." + taskID
	return h.publisher.PublishJSON(ctx, subject, status)
}
