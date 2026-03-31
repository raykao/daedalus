package registry

import (
	"strings"
	"testing"
)

const validJSON = `{
  "agents": [
    {
      "card": {
        "name": "alpha",
        "description": "Alpha agent",
        "version": "1.0.0",
        "defaultInputModes": ["text"],
        "defaultOutputModes": ["text"],
        "skills": [
          {"id": "code-review", "name": "Code Review", "description": "Reviews code", "tags": ["code", "review"]},
          {"id": "debugging", "name": "Debugging", "description": "Debugs code", "tags": ["code", "debug"]}
        ],
        "capabilities": {"streaming": false}
      },
      "queueSubject": "agent.tasks.alpha",
      "runtime": "runtime-a",
      "acpPort": 3000
    },
    {
      "card": {
        "name": "beta",
        "description": "Beta agent",
        "version": "2.0.0",
        "defaultInputModes": ["text"],
        "defaultOutputModes": ["text"],
        "skills": [
          {"id": "research", "name": "Research", "description": "Does research", "tags": ["research", "analysis"]},
          {"id": "code-review", "name": "Code Review", "description": "Reviews code", "tags": ["code", "review"]}
        ],
        "capabilities": {}
      },
      "queueSubject": "agent.tasks.beta",
      "runtime": "runtime-b",
      "acpPort": 3001
    }
  ]
}`

func TestLoadValidRegistry(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := len(reg.All()); got != 2 {
		t.Fatalf("expected 2 agents, got %d", got)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	_, err := LoadFromReader(strings.NewReader(`{not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestMissingName(t *testing.T) {
	j := `{"agents":[{"card":{"name":"","description":"d","version":"1","skills":[{"id":"s","name":"S","description":"x","tags":[]}]},"queueSubject":"q","runtime":"r","acpPort":1}]}`
	_, err := LoadFromReader(strings.NewReader(j))
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestMissingQueueSubject(t *testing.T) {
	j := `{"agents":[{"card":{"name":"a","description":"d","version":"1","skills":[{"id":"s","name":"S","description":"x","tags":[]}]},"queueSubject":"","runtime":"r","acpPort":1}]}`
	_, err := LoadFromReader(strings.NewReader(j))
	if err == nil {
		t.Fatal("expected error for missing queueSubject")
	}
}

func TestDuplicateName(t *testing.T) {
	j := `{"agents":[
		{"card":{"name":"dup","description":"d","version":"1","skills":[{"id":"s","name":"S","description":"x","tags":[]}]},"queueSubject":"q1","runtime":"r","acpPort":1},
		{"card":{"name":"dup","description":"d","version":"1","skills":[{"id":"s2","name":"S2","description":"x","tags":[]}]},"queueSubject":"q2","runtime":"r","acpPort":2}
	]}`
	_, err := LoadFromReader(strings.NewReader(j))
	if err == nil {
		t.Fatal("expected error for duplicate name")
	}
	if !strings.Contains(err.Error(), "duplicate agent name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestDuplicateQueueSubject(t *testing.T) {
	j := `{"agents":[
		{"card":{"name":"a","description":"d","version":"1","skills":[{"id":"s","name":"S","description":"x","tags":[]}]},"queueSubject":"same","runtime":"r","acpPort":1},
		{"card":{"name":"b","description":"d","version":"1","skills":[{"id":"s2","name":"S2","description":"x","tags":[]}]},"queueSubject":"same","runtime":"r","acpPort":2}
	]}`
	_, err := LoadFromReader(strings.NewReader(j))
	if err == nil {
		t.Fatal("expected error for duplicate queueSubject")
	}
	if !strings.Contains(err.Error(), "duplicate queueSubject") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestFindBySkill(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// code-review is in both alpha and beta
	matches := reg.FindBySkill("code-review")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for code-review, got %d", len(matches))
	}

	// debugging is only in alpha
	matches = reg.FindBySkill("debugging")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for debugging, got %d", len(matches))
	}
	if matches[0].Card.Name != "alpha" {
		t.Fatalf("expected alpha, got %s", matches[0].Card.Name)
	}

	// unknown skill
	matches = reg.FindBySkill("nonexistent")
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches for nonexistent, got %d", len(matches))
	}
}

func TestFindByTag(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// "code" tag appears in both agents
	matches := reg.FindByTag("code")
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for tag 'code', got %d", len(matches))
	}

	// "analysis" only in beta
	matches = reg.FindByTag("analysis")
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for tag 'analysis', got %d", len(matches))
	}
	if matches[0].Card.Name != "beta" {
		t.Fatalf("expected beta, got %s", matches[0].Card.Name)
	}
}

func TestFindByName(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	entry, ok := reg.FindByName("alpha")
	if !ok {
		t.Fatal("expected to find alpha")
	}
	if entry.Card.Name != "alpha" {
		t.Fatalf("expected alpha, got %s", entry.Card.Name)
	}

	_, ok = reg.FindByName("nonexistent")
	if ok {
		t.Fatal("expected not found for nonexistent")
	}
}

func TestRoute(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	subject, err := reg.Route("debugging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if subject != "agent.tasks.alpha" {
		t.Fatalf("expected agent.tasks.alpha, got %s", subject)
	}
}

func TestRouteUnknownSkill(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(validJSON))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err = reg.Route("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown skill")
	}
}

func TestEmptyRegistry(t *testing.T) {
	reg, err := LoadFromReader(strings.NewReader(`{"agents":[]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(reg.FindBySkill("anything")) != 0 {
		t.Fatal("expected empty result from FindBySkill on empty registry")
	}
	if len(reg.FindByTag("anything")) != 0 {
		t.Fatal("expected empty result from FindByTag on empty registry")
	}
	if _, ok := reg.FindByName("anything"); ok {
		t.Fatal("expected not found from FindByName on empty registry")
	}
	if _, err := reg.Route("anything"); err == nil {
		t.Fatal("expected error from Route on empty registry")
	}
	if len(reg.All()) != 0 {
		t.Fatal("expected empty All() on empty registry")
	}
}
