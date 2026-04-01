// Package contextmgmt provides context window usage tracking and R18
// resurrection strategy selection for the daedalus proxy sidecar.
package contextmgmt

import (
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ResurrectionStrategy indicates which session restoration approach to use.
type ResurrectionStrategy string

const (
	// StrategyFull restores the entire session (context usage < fullThreshold).
	StrategyFull ResurrectionStrategy = "full"

	// StrategyCheckpoint starts a fresh session with checkpoint summary injected
	// (context usage between fullThreshold and checkpointThreshold).
	StrategyCheckpoint ResurrectionStrategy = "checkpoint"

	// StrategyFresh starts a completely new session, injecting only the git diff
	// from the previous attempt (context usage > checkpointThreshold).
	StrategyFresh ResurrectionStrategy = "fresh"
)

// Config holds context management configuration, typically populated from
// environment variables injected by the operator.
type Config struct {
	CompactionInterval string
	TokenThreshold     int64
	EventRetentionSize int32
	OverlapSize        int32

	// Resurrection thresholds (percentage 0-100).
	ResurrectionFullThreshold       int32
	ResurrectionCheckpointThreshold int32
}

// DefaultConfig returns a Config with the CRD default values.
func DefaultConfig() Config {
	return Config{
		CompactionInterval:              "5m",
		TokenThreshold:                  100000,
		EventRetentionSize:              50,
		OverlapSize:                     2,
		ResurrectionFullThreshold:       60,
		ResurrectionCheckpointThreshold: 90,
	}
}

// SessionMetrics holds per-session context usage data.
type SessionMetrics struct {
	SessionID       string
	TaskID          string
	CurrentTokens   int64
	TokenLimit      int64
	TurnCount       int32
	CompactionCount int32
	LastCompaction  time.Time
	StartTime       time.Time
}

// UsagePercent returns the context usage as a percentage.
func (m *SessionMetrics) UsagePercent() int32 {
	if m.TokenLimit <= 0 {
		return 0
	}
	pct := float64(m.CurrentTokens) / float64(m.TokenLimit) * 100
	if pct > 100 {
		return 100
	}
	return int32(pct)
}

// NeedsCompaction returns true if the session exceeds the token threshold.
func (m *SessionMetrics) NeedsCompaction(threshold int64) bool {
	return m.CurrentTokens >= threshold
}

// Tracker tracks context usage across sessions and selects resurrection strategies.
type Tracker struct {
	config Config
	logger *slog.Logger

	mu       sync.Mutex
	sessions map[string]*SessionMetrics
}

// NewTracker creates a Tracker with the given config. It returns an error if
// the resurrection thresholds are misconfigured (fullThreshold must be less
// than checkpointThreshold).
func NewTracker(config Config, logger *slog.Logger) (*Tracker, error) {
	if config.ResurrectionFullThreshold >= config.ResurrectionCheckpointThreshold {
		return nil, fmt.Errorf("contextmgmt: fullThreshold (%d) must be less than checkpointThreshold (%d)",
			config.ResurrectionFullThreshold, config.ResurrectionCheckpointThreshold)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Tracker{
		config:   config,
		logger:   logger,
		sessions: make(map[string]*SessionMetrics),
	}, nil
}

// StartSession begins tracking a new session.
func (t *Tracker) StartSession(sessionID, taskID string, tokenLimit int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions[sessionID] = &SessionMetrics{
		SessionID:  sessionID,
		TaskID:     taskID,
		TokenLimit: tokenLimit,
		StartTime:  time.Now(),
	}
	t.logger.Info("context: session tracking started",
		"sessionId", sessionID,
		"taskId", taskID,
		"tokenLimit", tokenLimit,
	)
}

// UpdateTokens updates the token count for a session. Called after each
// prompt/response cycle when the bridge reports token usage.
func (t *Tracker) UpdateTokens(sessionID string, tokens int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.sessions[sessionID]
	if !ok {
		return
	}
	m.CurrentTokens = tokens
	m.TurnCount++
	t.logger.Debug("context: tokens updated",
		"sessionId", sessionID,
		"tokens", tokens,
		"turnCount", m.TurnCount,
		"usagePercent", m.UsagePercent(),
	)
}

// RecordCompaction records that a compaction event occurred.
func (t *Tracker) RecordCompaction(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.sessions[sessionID]
	if !ok {
		return
	}
	m.CompactionCount++
	m.LastCompaction = time.Now()
	t.logger.Info("context: compaction recorded",
		"sessionId", sessionID,
		"compactionCount", m.CompactionCount,
	)
}

// GetMetrics returns a copy of the metrics for a session.
func (t *Tracker) GetMetrics(sessionID string) (SessionMetrics, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.sessions[sessionID]
	if !ok {
		return SessionMetrics{}, false
	}
	return *m, true
}

// EndSession stops tracking and returns the final metrics.
func (t *Tracker) EndSession(sessionID string) (SessionMetrics, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.sessions[sessionID]
	if !ok {
		return SessionMetrics{}, false
	}
	result := *m
	delete(t.sessions, sessionID)
	t.logger.Info("context: session tracking ended",
		"sessionId", sessionID,
		"finalTokens", result.CurrentTokens,
		"turnCount", result.TurnCount,
		"usagePercent", result.UsagePercent(),
	)
	return result, true
}

// SelectResurrectionStrategy implements the R18 decision tree.
// Given context usage percentage, returns which strategy to use.
func (t *Tracker) SelectResurrectionStrategy(usagePercent int32) ResurrectionStrategy {
	switch {
	case usagePercent < t.config.ResurrectionFullThreshold:
		return StrategyFull
	case usagePercent < t.config.ResurrectionCheckpointThreshold:
		return StrategyCheckpoint
	default:
		return StrategyFresh
	}
}

// SelectResurrectionStrategyForSession selects resurrection strategy based on
// the tracked metrics of a previous session.
func (t *Tracker) SelectResurrectionStrategyForSession(sessionID string) (ResurrectionStrategy, error) {
	metrics, ok := t.GetMetrics(sessionID)
	if !ok {
		return StrategyFresh, fmt.Errorf("context: no metrics for session %s, defaulting to fresh", sessionID)
	}
	strategy := t.SelectResurrectionStrategy(metrics.UsagePercent())
	t.logger.Info("context: resurrection strategy selected",
		"sessionId", sessionID,
		"usagePercent", metrics.UsagePercent(),
		"strategy", strategy,
	)
	return strategy, nil
}

// CheckCompaction checks if any tracked session needs compaction and returns
// the session IDs that should be compacted.
func (t *Tracker) CheckCompaction() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var needsCompaction []string
	for id, m := range t.sessions {
		if m.NeedsCompaction(t.config.TokenThreshold) {
			needsCompaction = append(needsCompaction, id)
		}
	}
	return needsCompaction
}

// ActiveSessionCount returns the number of actively tracked sessions.
func (t *Tracker) ActiveSessionCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sessions)
}
