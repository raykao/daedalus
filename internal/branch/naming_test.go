package branch

import (
	"testing"
)

func TestNew_Valid(t *testing.T) {
	b, err := New("fix-login-bug", "copilot", "abc123def456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Feature != "fix-login-bug" {
		t.Errorf("Feature = %q, want %q", b.Feature, "fix-login-bug")
	}
	if b.AgentName != "copilot" {
		t.Errorf("AgentName = %q, want %q", b.AgentName, "copilot")
	}
	if b.SessionID != "abc123def456" {
		t.Errorf("SessionID = %q, want %q", b.SessionID, "abc123def456")
	}
}

func TestNew_EmptyAfterSanitize(t *testing.T) {
	_, err := New("~^:", "copilot", "abc")
	if err == nil {
		t.Fatal("expected error for feature that sanitizes to empty")
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		feature, agent, session, want string
	}{
		{"fix-login-bug", "copilot", "abc123def456", "agent/fix-login-bug/copilot/abc123def456"},
		{"add-api-tests", "claude", "sess-789", "agent/add-api-tests/claude/sess-789"},
		{"refactor-db", "codex", "a1b2c3d4", "agent/refactor-db/codex/a1b2c3d4"},
	}
	for _, tt := range tests {
		b := BranchName{Feature: tt.feature, AgentName: tt.agent, SessionID: tt.session}
		if got := b.String(); got != tt.want {
			t.Errorf("String() = %q, want %q", got, tt.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	original, err := New("fix-login-bug", "copilot", "abc123def456")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parsed, err := Parse(original.String())
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if parsed != original {
		t.Errorf("round-trip mismatch: got %+v, want %+v", parsed, original)
	}
}

func TestParse_Valid(t *testing.T) {
	b, err := Parse("agent/fix-login-bug/copilot/abc123def456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Feature != "fix-login-bug" || b.AgentName != "copilot" || b.SessionID != "abc123def456" {
		t.Errorf("unexpected result: %+v", b)
	}
}

func TestParse_StripOriginPrefix(t *testing.T) {
	b, err := Parse("origin/agent/fix-login-bug/copilot/abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Feature != "fix-login-bug" {
		t.Errorf("Feature = %q", b.Feature)
	}
}

func TestParse_MissingPrefix(t *testing.T) {
	_, err := Parse("fix-login-bug/copilot/abc123")
	if err == nil {
		t.Fatal("expected error for missing prefix")
	}
}

func TestParse_TooFewSegments(t *testing.T) {
	_, err := Parse("agent/fix-login-bug/copilot")
	if err == nil {
		t.Fatal("expected error for too few segments")
	}
}

func TestParse_EmptyComponent(t *testing.T) {
	_, err := Parse("agent//copilot/abc123")
	if err == nil {
		t.Fatal("expected error for empty component")
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"fix login bug", "fix-login-bug"},
		{"feat.new", "feat-new"},
		{"add:api", "add-api"},
		{"test~thing", "test-thing"},
		{"a  b", "a-b"},
		{"--leading", "leading"},
		{"trailing--", "trailing"},
		{"hello/world", "hello-world"},
		{"normal-name", "normal-name"},
		{"UPPERCASE", "UPPERCASE"},
		{"with@symbol", "with-symbol"},
	}
	for _, tt := range tests {
		if got := Sanitize(tt.input); got != tt.want {
			t.Errorf("Sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitize_Unicode(t *testing.T) {
	// Unicode letters should pass through; non-letter symbols should not
	got := Sanitize("cafe-\u00e9")
	// \u00e9 is 'e with accent' - a valid unicode letter, should pass
	if got != "cafe-\u00e9" {
		t.Errorf("Sanitize(cafe-e-accent) = %q, want %q", got, "cafe-\u00e9")
	}
}

func TestNew_LongName(t *testing.T) {
	long := "a"
	for i := 0; i < 200; i++ {
		long += "x"
	}
	b, err := New(long, "copilot", "sess1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still be valid (git supports long branch names)
	if b.Feature != long {
		t.Errorf("Feature truncated unexpectedly")
	}
}
