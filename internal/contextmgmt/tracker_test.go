package contextmgmt

import "testing"

func TestSessionMetricsUsagePercent(t *testing.T) {
	tests := []struct {
		name        string
		current     int64
		limit       int64
		wantPercent int32
	}{
		{"zero usage", 0, 100000, 0},
		{"half usage", 50000, 100000, 50},
		{"full usage", 100000, 100000, 100},
		{"over limit", 150000, 100000, 100},
		{"zero limit", 50000, 0, 0},
		{"small usage", 1000, 100000, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &SessionMetrics{CurrentTokens: tt.current, TokenLimit: tt.limit}
			got := m.UsagePercent()
			if got != tt.wantPercent {
				t.Errorf("UsagePercent() = %d, want %d", got, tt.wantPercent)
			}
		})
	}
}

func TestNeedsCompaction(t *testing.T) {
	tests := []struct {
		name      string
		current   int64
		threshold int64
		want      bool
	}{
		{"below threshold", 50000, 100000, false},
		{"at threshold", 100000, 100000, true},
		{"above threshold", 150000, 100000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &SessionMetrics{CurrentTokens: tt.current}
			if got := m.NeedsCompaction(tt.threshold); got != tt.want {
				t.Errorf("NeedsCompaction(%d) = %v, want %v", tt.threshold, got, tt.want)
			}
		})
	}
}

func TestTrackerSessionLifecycle(t *testing.T) {
	tracker := NewTracker(DefaultConfig(), nil)

	// Start session
	tracker.StartSession("sess-1", "task-1", 128000)
	if tracker.ActiveSessionCount() != 1 {
		t.Fatalf("expected 1 active session, got %d", tracker.ActiveSessionCount())
	}

	// Update tokens
	tracker.UpdateTokens("sess-1", 50000)
	m, ok := tracker.GetMetrics("sess-1")
	if !ok {
		t.Fatal("expected metrics for sess-1")
	}
	if m.CurrentTokens != 50000 {
		t.Errorf("expected 50000 tokens, got %d", m.CurrentTokens)
	}
	if m.TurnCount != 1 {
		t.Errorf("expected 1 turn, got %d", m.TurnCount)
	}

	// Record compaction
	tracker.RecordCompaction("sess-1")
	m, _ = tracker.GetMetrics("sess-1")
	if m.CompactionCount != 1 {
		t.Errorf("expected 1 compaction, got %d", m.CompactionCount)
	}

	// End session
	final, ok := tracker.EndSession("sess-1")
	if !ok {
		t.Fatal("expected final metrics")
	}
	if final.CurrentTokens != 50000 {
		t.Errorf("expected 50000 final tokens, got %d", final.CurrentTokens)
	}
	if tracker.ActiveSessionCount() != 0 {
		t.Fatalf("expected 0 active sessions, got %d", tracker.ActiveSessionCount())
	}
}

func TestTrackerUpdateNonexistentSession(t *testing.T) {
	tracker := NewTracker(DefaultConfig(), nil)
	// Should not panic
	tracker.UpdateTokens("nonexistent", 50000)
	tracker.RecordCompaction("nonexistent")
}

func TestResurrectionStrategy(t *testing.T) {
	tracker := NewTracker(DefaultConfig(), nil) // defaults: full<60, checkpoint<90

	tests := []struct {
		name  string
		usage int32
		want  ResurrectionStrategy
	}{
		{"low usage - full", 30, StrategyFull},
		{"at full threshold boundary", 59, StrategyFull},
		{"at full threshold", 60, StrategyCheckpoint},
		{"mid usage - checkpoint", 75, StrategyCheckpoint},
		{"at checkpoint boundary", 89, StrategyCheckpoint},
		{"at checkpoint threshold", 90, StrategyFresh},
		{"high usage - fresh", 95, StrategyFresh},
		{"max usage", 100, StrategyFresh},
		{"zero usage", 0, StrategyFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tracker.SelectResurrectionStrategy(tt.usage)
			if got != tt.want {
				t.Errorf("SelectResurrectionStrategy(%d) = %s, want %s", tt.usage, got, tt.want)
			}
		})
	}
}

func TestResurrectionStrategyCustomThresholds(t *testing.T) {
	cfg := Config{
		ResurrectionFullThreshold:       40,
		ResurrectionCheckpointThreshold: 70,
	}
	tracker := NewTracker(cfg, nil)

	if got := tracker.SelectResurrectionStrategy(39); got != StrategyFull {
		t.Errorf("expected full at 39%%, got %s", got)
	}
	if got := tracker.SelectResurrectionStrategy(40); got != StrategyCheckpoint {
		t.Errorf("expected checkpoint at 40%%, got %s", got)
	}
	if got := tracker.SelectResurrectionStrategy(70); got != StrategyFresh {
		t.Errorf("expected fresh at 70%%, got %s", got)
	}
}

func TestCheckCompaction(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TokenThreshold = 80000
	tracker := NewTracker(cfg, nil)

	tracker.StartSession("sess-low", "task-1", 128000)
	tracker.UpdateTokens("sess-low", 50000)

	tracker.StartSession("sess-high", "task-2", 128000)
	tracker.UpdateTokens("sess-high", 90000)

	needs := tracker.CheckCompaction()
	if len(needs) != 1 {
		t.Fatalf("expected 1 session needing compaction, got %d", len(needs))
	}
	if needs[0] != "sess-high" {
		t.Errorf("expected sess-high, got %s", needs[0])
	}
}

func TestResurrectionStrategyForSessionNotFound(t *testing.T) {
	tracker := NewTracker(DefaultConfig(), nil)
	strategy, err := tracker.SelectResurrectionStrategyForSession("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
	if strategy != StrategyFresh {
		t.Errorf("expected StrategyFresh as default, got %s", strategy)
	}
}

func TestResurrectionStrategyForSession(t *testing.T) {
	tracker := NewTracker(DefaultConfig(), nil)

	// Start a session with known token limit and update tokens to 40% usage
	tracker.StartSession("sess-test", "task-test", 100000)
	tracker.UpdateTokens("sess-test", 40000)

	strategy, err := tracker.SelectResurrectionStrategyForSession("sess-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strategy != StrategyFull {
		t.Errorf("expected StrategyFull at 40%% usage, got %s", strategy)
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.TokenThreshold != 100000 {
		t.Errorf("expected default TokenThreshold 100000, got %d", cfg.TokenThreshold)
	}
	if cfg.EventRetentionSize != 50 {
		t.Errorf("expected default EventRetentionSize 50, got %d", cfg.EventRetentionSize)
	}
	if cfg.OverlapSize != 2 {
		t.Errorf("expected default OverlapSize 2, got %d", cfg.OverlapSize)
	}
	if cfg.ResurrectionFullThreshold != 60 {
		t.Errorf("expected default ResurrectionFullThreshold 60, got %d", cfg.ResurrectionFullThreshold)
	}
	if cfg.ResurrectionCheckpointThreshold != 90 {
		t.Errorf("expected default ResurrectionCheckpointThreshold 90, got %d", cfg.ResurrectionCheckpointThreshold)
	}
	if cfg.CompactionInterval != "5m" {
		t.Errorf("expected default CompactionInterval 5m, got %s", cfg.CompactionInterval)
	}
}
