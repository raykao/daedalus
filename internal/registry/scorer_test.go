package registry

import (
	"math"
	"strings"
	"testing"

	"github.com/raykao/agent-forge/internal/a2a"
)

func newEntry(name, runtime string, skills []a2a.AgentSkill) AgentEntry {
	return AgentEntry{
		Card: a2a.AgentCard{
			Name:        name,
			Description: name + " agent",
			Version:     "1.0.0",
			Skills:      skills,
		},
		QueueSubject: "agent.tasks." + name,
		Runtime:      runtime,
		ACPPort:      3000,
	}
}

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestScoreExactSkillMatch(t *testing.T) {
	entry := newEntry("a", "r", []a2a.AgentSkill{
		{ID: "code-review", Name: "Code Review", Tags: []string{"code"}},
	})
	s := Score(entry, RoutingRequest{SkillID: "code-review"})
	// 1.0 (skill) -- no tag bonus since Tags in request is empty
	if !approxEqual(s, 1.0) {
		t.Fatalf("expected 1.0, got %f", s)
	}
}

func TestScoreExactSkillMatchWithTagBonus(t *testing.T) {
	entry := newEntry("a", "r", []a2a.AgentSkill{
		{ID: "code-review", Name: "Code Review", Tags: []string{"code", "review"}},
	})
	s := Score(entry, RoutingRequest{SkillID: "code-review", Tags: []string{"code"}})
	// 1.0 (skill) + 0.5 (one tag match) = 1.5
	if !approxEqual(s, 1.5) {
		t.Fatalf("expected 1.5, got %f", s)
	}
}

func TestScoreTagOnlyMatch(t *testing.T) {
	entry := newEntry("a", "r", []a2a.AgentSkill{
		{ID: "research", Name: "Research", Tags: []string{"analysis", "research"}},
	})
	s := Score(entry, RoutingRequest{Tags: []string{"analysis"}})
	if !approxEqual(s, 0.5) {
		t.Fatalf("expected 0.5, got %f", s)
	}
}

func TestScoreRuntimePreference(t *testing.T) {
	entry := newEntry("a", "copilot-bridge", []a2a.AgentSkill{
		{ID: "code-review", Name: "Code Review", Tags: []string{"code"}},
	})
	s := Score(entry, RoutingRequest{SkillID: "code-review", PreferredRuntime: "copilot-bridge"})
	// 1.0 + 0.1 = 1.1
	if !approxEqual(s, 1.1) {
		t.Fatalf("expected 1.1, got %f", s)
	}
}

func TestScoreMultipleTagsAccumulate(t *testing.T) {
	entry := newEntry("a", "r", []a2a.AgentSkill{
		{ID: "s1", Name: "S1", Tags: []string{"code", "review"}},
		{ID: "s2", Name: "S2", Tags: []string{"quality", "testing"}},
	})
	s := Score(entry, RoutingRequest{Tags: []string{"code", "review", "quality"}})
	// 0.5 * 3 = 1.5
	if !approxEqual(s, 1.5) {
		t.Fatalf("expected 1.5, got %f", s)
	}
}

func TestRouteByScoreHighest(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// alpha has debugging, beta does not; both have code-review.
	// Request debugging skill: alpha should win.
	best, err := reg.RouteByScore(RoutingRequest{SkillID: "debugging"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best.Card.Name != "alpha" {
		t.Fatalf("expected alpha, got %s", best.Card.Name)
	}
}

func TestRouteByScoreNoMatch(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err = reg.RouteByScore(RoutingRequest{SkillID: "nonexistent"})
	if err == nil {
		t.Fatal("expected error when no agent matches")
	}
}

func TestRouteByScorePreferRuntime(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Both have code-review (score 1.0 each). Prefer runtime-b to pick beta.
	best, err := reg.RouteByScore(RoutingRequest{SkillID: "code-review", PreferredRuntime: "runtime-b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if best.Card.Name != "beta" {
		t.Fatalf("expected beta (runtime-b preference), got %s", best.Card.Name)
	}
}
