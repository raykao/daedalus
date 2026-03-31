package branch

import (
	"context"
	"testing"
)

func mockLister(refs []string) BranchLister {
	return func(_ context.Context) ([]string, error) {
		return refs, nil
	}
}

func TestFindRetries_NoMatches(t *testing.T) {
	d := NewDetector(mockLister([]string{
		"agent/other-feature/copilot/sess1",
		"agent/fix-login-bug/claude/sess2",
	}))
	matches, err := d.FindRetries(context.Background(), "fix-login-bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matches))
	}
}

func TestFindRetries_WithMatches(t *testing.T) {
	d := NewDetector(mockLister([]string{
		"agent/fix-login-bug/copilot/sess1",
		"agent/fix-login-bug/copilot/sess2",
		"agent/other-feature/copilot/sess3",
	}))
	matches, err := d.FindRetries(context.Background(), "fix-login-bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 2 {
		t.Errorf("expected 2 matches, got %d", len(matches))
	}
}

func TestIsRetry_NoRetry(t *testing.T) {
	d := NewDetector(mockLister([]string{}))
	isRetry, count, err := d.IsRetry(context.Background(), "fix-login-bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isRetry {
		t.Error("expected isRetry = false")
	}
	if count != 0 {
		t.Errorf("expected count = 0, got %d", count)
	}
}

func TestIsRetry_WithRetries(t *testing.T) {
	d := NewDetector(mockLister([]string{
		"agent/fix-login-bug/copilot/sess1",
		"agent/fix-login-bug/copilot/sess2",
		"agent/fix-login-bug/copilot/sess3",
	}))
	isRetry, count, err := d.IsRetry(context.Background(), "fix-login-bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isRetry {
		t.Error("expected isRetry = true")
	}
	if count != 3 {
		t.Errorf("expected count = 3, got %d", count)
	}
}

func TestLatestSession_None(t *testing.T) {
	d := NewDetector(mockLister([]string{}))
	latest, err := d.LatestSession(context.Background(), "fix-login-bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if latest != nil {
		t.Errorf("expected nil, got %+v", latest)
	}
}

func TestLatestSession_One(t *testing.T) {
	d := NewDetector(mockLister([]string{
		"agent/fix-login-bug/copilot/sess1",
	}))
	latest, err := d.LatestSession(context.Background(), "fix-login-bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil result")
	}
	if latest.SessionID != "sess1" {
		t.Errorf("SessionID = %q, want %q", latest.SessionID, "sess1")
	}
}

func TestLatestSession_Multiple(t *testing.T) {
	d := NewDetector(mockLister([]string{
		"agent/fix-login-bug/copilot/sess1",
		"agent/fix-login-bug/copilot/sess2",
		"agent/fix-login-bug/copilot/sess3",
	}))
	latest, err := d.LatestSession(context.Background(), "fix-login-bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if latest == nil {
		t.Fatal("expected non-nil result")
	}
	if latest.SessionID != "sess3" {
		t.Errorf("SessionID = %q, want %q (last in list)", latest.SessionID, "sess3")
	}
}

func TestFindRetries_SanitizesInputs(t *testing.T) {
	d := NewDetector(mockLister([]string{
		"agent/fix-login-bug/copilot/sess1",
	}))
	// "fix login bug" sanitizes to "fix-login-bug"
	matches, err := d.FindRetries(context.Background(), "fix login bug", "copilot")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("expected 1 match after sanitization, got %d", len(matches))
	}
}
