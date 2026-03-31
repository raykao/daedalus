package contract

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// registryAgent mirrors the structure of an agent entry in agent-registry.json.
type registryAgent struct {
	Card         map[string]any `json:"card"`
	QueueSubject string         `json:"queueSubject,omitempty"`
	Runtime      string         `json:"runtime,omitempty"`
	ACPPort      int            `json:"acpPort,omitempty"`
}

// registryFile mirrors the top-level structure of agent-registry.json.
type registryFile struct {
	Agents []registryAgent `json:"agents"`
}

// checkUniqueAgentNames returns an error if any two agents share the same card name.
func checkUniqueAgentNames(data []byte) error {
	var reg registryFile
	if err := json.Unmarshal(data, &reg); err != nil {
		return fmt.Errorf("parsing registry: %w", err)
	}
	seen := make(map[string]bool)
	for i, a := range reg.Agents {
		name, ok := a.Card["name"].(string)
		if !ok || name == "" {
			return fmt.Errorf("agent at index %d has missing or invalid name field", i)
		}
		if seen[name] {
			return fmt.Errorf("duplicate agent name: %q", name)
		}
		seen[name] = true
	}
	return nil
}

func TestAgentRegistryFormat(t *testing.T) {
	schema, err := loadSchema("agent-registry.schema.json")
	if err != nil {
		t.Fatalf("loading agent-registry schema: %v", err)
	}

	t.Run("valid actual registry file", func(t *testing.T) {
		data, err := os.ReadFile("../../deploy/config/agent-registry.json")
		if err != nil {
			t.Fatalf("reading agent-registry.json: %v", err)
		}
		if err := validateJSON(schema, data); err != nil {
			t.Errorf("actual registry failed schema validation: %v", err)
		}
		if err := checkUniqueAgentNames(data); err != nil {
			t.Errorf("actual registry has duplicate agent names: %v", err)
		}
	})

	t.Run("invalid missing queueSubject", func(t *testing.T) {
		input := []byte(`{
			"agents": [
				{
					"card": {
						"name": "test",
						"description": "test agent",
						"version": "1.0.0",
						"defaultInputModes": ["text"],
						"defaultOutputModes": ["text"],
						"skills": [{"id": "s1", "name": "S", "description": "d", "tags": []}]
					},
					"runtime": "test-runtime",
					"acpPort": 3000
				}
			]
		}`)
		if err := validateJSON(schema, input); err == nil {
			t.Error("expected validation error for missing queueSubject, got nil")
		}
	})

	t.Run("invalid missing card", func(t *testing.T) {
		input := []byte(`{
			"agents": [
				{
					"queueSubject": "agent.tasks.test",
					"runtime": "test-runtime",
					"acpPort": 3000
				}
			]
		}`)
		if err := validateJSON(schema, input); err == nil {
			t.Error("expected validation error for missing card, got nil")
		}
	})

	t.Run("invalid duplicate agent names", func(t *testing.T) {
		input := []byte(`{
			"agents": [
				{
					"card": {
						"name": "duplicate-agent",
						"description": "first instance",
						"version": "1.0.0",
						"defaultInputModes": ["text"],
						"defaultOutputModes": ["text"],
						"skills": [{"id": "s1", "name": "S", "description": "d", "tags": []}]
					},
					"queueSubject": "agent.tasks.duplicate-agent",
					"runtime": "test-runtime",
					"acpPort": 3000
				},
				{
					"card": {
						"name": "duplicate-agent",
						"description": "second instance",
						"version": "1.0.0",
						"defaultInputModes": ["text"],
						"defaultOutputModes": ["text"],
						"skills": [{"id": "s1", "name": "S", "description": "d", "tags": []}]
					},
					"queueSubject": "agent.tasks.duplicate-agent",
					"runtime": "test-runtime",
					"acpPort": 3001
				}
			]
		}`)
		if err := checkUniqueAgentNames(input); err == nil {
			t.Error("expected duplicate name error, got nil")
		}
	})
}
