package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"

	"github.com/raykao/daedalus/internal/a2a"
	"github.com/raykao/daedalus/internal/registry"
)

// MessagePublisher is the interface Dispatcher uses to publish messages.
// queue.Publisher satisfies this interface.
type MessagePublisher interface {
	PublishJSON(ctx context.Context, subject string, v interface{}) error
}

// TaskDispatcher is the interface Orchestrator uses to dispatch individual tasks.
// Dispatcher satisfies this interface.
type TaskDispatcher interface {
	Dispatch(ctx context.Context, spec TaskSpec, contextID string) (taskID string, subject string, err error)
}

// Dispatcher handles publishing tasks to the correct NATS subjects and
// tracking their initial dispatch status.
type Dispatcher struct {
	publisher MessagePublisher
	registry  *registry.Registry
	logger    *slog.Logger
}

// NewDispatcher creates a Dispatcher with the given publisher, registry, and logger.
func NewDispatcher(publisher MessagePublisher, reg *registry.Registry, logger *slog.Logger) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		publisher: publisher,
		registry:  reg,
		logger:    logger,
	}
}

// Dispatch sends a single TaskSpec to the appropriate agent's NATS subject.
// It routes via the registry (skill matching), builds an A2A SendMessageRequest,
// and publishes to the agent's queue subject.
// Returns the task ID and the subject it was dispatched to.
func (d *Dispatcher) Dispatch(ctx context.Context, spec TaskSpec, contextID string) (taskID string, subject string, err error) {
	// 1. Generate task ID if not provided.
	taskID = spec.ID
	if taskID == "" {
		suffix, genErr := generateID()
		if genErr != nil {
			return "", "", fmt.Errorf("dispatch: generate task ID: %w", genErr)
		}
		if contextID != "" {
			taskID = contextID + "-" + suffix
		} else {
			taskID = suffix
		}
	}

	// 2. Route via registry.
	entry, routeErr := d.registry.RouteByScore(registry.RoutingRequest{
		SkillID: spec.SkillID,
		Tags:    spec.Tags,
	})
	if routeErr != nil {
		return "", "", fmt.Errorf("dispatch: route task %s (skill=%s): %w", taskID, spec.SkillID, routeErr)
	}
	subject = entry.QueueSubject

	// 3. Build A2A SendMessageRequest.
	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: taskID,
			TaskID:    taskID,
			ContextID: contextID,
			Role:      "user",
			Parts: []a2a.Part{
				{Text: spec.Prompt},
			},
			Metadata: spec.Metadata,
		},
	}

	// 4. Publish to the agent's queue subject.
	if pubErr := d.publisher.PublishJSON(ctx, subject, req); pubErr != nil {
		return "", "", fmt.Errorf("dispatch: publish task %s to %s: %w", taskID, subject, pubErr)
	}

	d.logger.Info("dispatch: task published",
		"taskID", taskID,
		"contextID", contextID,
		"skillID", spec.SkillID,
		"subject", subject,
	)

	return taskID, subject, nil
}

// generateID produces a random 8-byte hex string for use as a task ID suffix.
func generateID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
