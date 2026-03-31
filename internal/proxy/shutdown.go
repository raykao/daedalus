package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	// DefaultGracePeriod matches the recommended Kubernetes app grace window.
	// Set terminationGracePeriodSeconds to 35s (30s app + 5s K8s buffer).
	DefaultGracePeriod = 30 * time.Second

	// forceExitBuffer is the additional window given after ACP session cancels
	// are sent, to let in-flight Handle calls finish cleanly.
	forceExitBuffer = 5 * time.Second
)

// ShutdownManager coordinates graceful proxy shutdown in four phases:
//
//  1. Stop accepting new messages - caller cancels the consumer context.
//  2. Wait for in-flight Handle calls to complete within the grace period.
//  3. Cancel any remaining ACP sessions (if grace period expired).
//  4. Wait briefly for Handle calls to finish after cancellation, then return.
//
// The manager owns a "work context" (WorkContext) that is separate from the
// NATS consumer context. Passing this context to Handle - rather than the
// consumer context - ensures in-flight ACP operations are not cancelled the
// moment SIGTERM arrives; they get the full grace period instead.
type ShutdownManager struct {
	handler     *Handler
	gracePeriod time.Duration
	logger      *slog.Logger

	// workCtx is passed to Handle for ACP operations. It is NOT cancelled by
	// SIGTERM; only Shutdown() cancels it after the grace period.
	workCtx    context.Context
	workCancel context.CancelFunc
}

// NewShutdownManager creates a ShutdownManager. If gracePeriod <= 0,
// DefaultGracePeriod is used.
func NewShutdownManager(handler *Handler, gracePeriod time.Duration, logger *slog.Logger) *ShutdownManager {
	if gracePeriod <= 0 {
		gracePeriod = DefaultGracePeriod
	}
	if logger == nil {
		logger = slog.Default()
	}
	workCtx, workCancel := context.WithCancel(context.Background())
	return &ShutdownManager{
		handler:     handler,
		gracePeriod: gracePeriod,
		logger:      logger,
		workCtx:     workCtx,
		workCancel:  workCancel,
	}
}

// WorkContext returns the context that Handle should use for ACP operations.
// Unlike the NATS consumer context, this context survives SIGTERM and is only
// cancelled by Shutdown() after the grace period.
func (sm *ShutdownManager) WorkContext() context.Context {
	return sm.workCtx
}

// Shutdown orchestrates graceful shutdown. The caller must have already stopped
// new message ingestion (by cancelling the consumer context) before calling
// Shutdown.
//
// Shutdown blocks until one of three outcomes:
//   - All in-flight Handle calls complete within gracePeriod: returns nil.
//   - Grace period expires: sends ACP session/cancel for every active session,
//     then waits up to forceExitBuffer for Handle calls to drain. Returns nil
//     if they drain, or an error if forceExitBuffer also expires.
//   - ctx is cancelled before either of the above: returns ctx.Err().
func (sm *ShutdownManager) Shutdown(ctx context.Context) error {
	sm.logger.Info("proxy: shutdown initiated",
		"grace_period", sm.gracePeriod,
		"phase", "wait_inflight",
	)

	// Set shuttingDown before starting the WaitGroup goroutine. This prevents
	// new Handle calls from calling wg.Add after Wait returns, which would panic.
	sm.handler.shuttingDown.Store(true)

	// Phase 2: wait for all in-flight Handle calls within the grace period.
	graceCtx, graceCancel := context.WithTimeout(ctx, sm.gracePeriod)
	defer graceCancel()

	done := make(chan struct{})
	go func() {
		sm.handler.wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		sm.workCancel()
		return ctx.Err()

	case <-done:
		sm.logger.Info("proxy: shutdown complete - all in-flight messages finished cleanly")
		sm.workCancel()
		return nil

	case <-graceCtx.Done():
		sm.logger.Warn("proxy: grace period expired",
			"phase", "cancel_sessions",
			"active_sessions", len(sm.handler.activeSessions()),
		)
	}

	// Phase 3: cancel work context so Prompt calls return, then send
	// ACP session/cancel to notify the agent.
	sm.workCancel()
	sm.handler.CancelActiveSessions(ctx)

	// Phase 4: give a brief window for Handle goroutines to finish after cancel.
	// A new channel is used here rather than reusing `done` from phase 2: the
	// phase-2 goroutine may have already closed `done` between when graceCtx
	// fired and now, which would cause the select to match immediately and skip
	// the exit buffer window entirely.
	cancelDone := make(chan struct{})
	go func() {
		sm.handler.wg.Wait()
		close(cancelDone)
	}()

	exitTimer := time.NewTimer(forceExitBuffer)
	defer exitTimer.Stop()

	select {
	case <-cancelDone:
		sm.logger.Info("proxy: shutdown complete after session cancellation")
		return nil
	case <-exitTimer.C:
		sm.logger.Warn("proxy: force shutdown - handles did not drain within exit buffer")
		return fmt.Errorf("proxy: shutdown timed out: in-flight handles did not complete after session cancellation")
	case <-ctx.Done():
		return ctx.Err()
	}
}
