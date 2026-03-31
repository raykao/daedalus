package contract

import (
	"testing"

	"github.com/raykao/agent-forge/internal/a2a"
)

func TestAgentCardFormat(t *testing.T) {
	schema, err := loadSchema("agent-card.schema.json")
	if err != nil {
		t.Fatalf("loading agent-card schema: %v", err)
	}

	minimalCard := a2a.AgentCard{
		Name:               "test-agent",
		Description:        "A test agent",
		Version:            "1.0.0",
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []a2a.AgentSkill{
			{
				ID:          "skill-1",
				Name:        "Test Skill",
				Description: "Does something useful",
				Tags:        []string{"test"},
			},
		},
	}

	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name:    "valid minimal card",
			input:   mustMarshal(minimalCard),
			wantErr: false,
		},
		{
			name: "valid card with multiple skills",
			input: mustMarshal(a2a.AgentCard{
				Name:               "multi-skill-agent",
				Description:        "Agent with multiple skills",
				Version:            "2.0.0",
				DefaultInputModes:  []string{"text"},
				DefaultOutputModes: []string{"text"},
				Skills: []a2a.AgentSkill{
					{
						ID:          "skill-a",
						Name:        "Skill A",
						Description: "First skill",
						Tags:        []string{"alpha"},
						Examples:    []string{"example A"},
					},
					{
						ID:          "skill-b",
						Name:        "Skill B",
						Description: "Second skill",
						Tags:        []string{"beta"},
					},
				},
			}),
			wantErr: false,
		},
		{
			name: "valid card with capabilities",
			input: mustMarshal(a2a.AgentCard{
				Name:               "streaming-agent",
				Description:        "An agent with streaming",
				Version:            "1.0.0",
				DefaultInputModes:  []string{"text"},
				DefaultOutputModes: []string{"text"},
				Skills: []a2a.AgentSkill{
					{
						ID:          "stream-skill",
						Name:        "Stream",
						Description: "Streams output",
						Tags:        []string{"stream"},
					},
				},
				Capabilities: a2a.AgentCapabilities{
					Streaming:         true,
					PushNotifications: false,
				},
			}),
			wantErr: false,
		},
		{
			name:    "invalid missing name",
			input:   []byte(`{"description":"no name","version":"1.0.0","defaultInputModes":["text"],"defaultOutputModes":["text"],"skills":[{"id":"s1","name":"S","description":"d","tags":[]}]}`),
			wantErr: true,
		},
		{
			name:    "invalid missing description",
			input:   []byte(`{"name":"agent","version":"1.0.0","defaultInputModes":["text"],"defaultOutputModes":["text"],"skills":[{"id":"s1","name":"S","description":"d","tags":[]}]}`),
			wantErr: true,
		},
		{
			name:    "invalid empty skills array",
			input:   []byte(`{"name":"agent","description":"desc","version":"1.0.0","defaultInputModes":["text"],"defaultOutputModes":["text"],"skills":[]}`),
			wantErr: true,
		},
		{
			name:    "invalid skill missing id",
			input:   []byte(`{"name":"agent","description":"desc","version":"1.0.0","defaultInputModes":["text"],"defaultOutputModes":["text"],"skills":[{"name":"S","description":"d","tags":[]}]}`),
			wantErr: true,
		},
		{
			name:    "invalid empty defaultInputModes",
			input:   []byte(`{"name":"test","description":"test","version":"1.0","defaultInputModes":[],"defaultOutputModes":["text"],"skills":[{"id":"s1","name":"S","description":"D","tags":["t"]}]}`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJSON(schema, tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
