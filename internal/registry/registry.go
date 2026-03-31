package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/raykao/agent-forge/internal/a2a"
)

// AgentEntry binds an AgentCard to its infrastructure config.
type AgentEntry struct {
	Card         a2a.AgentCard `json:"card"`
	QueueSubject string        `json:"queueSubject"`
	Runtime      string        `json:"runtime"`
	ACPPort      int           `json:"acpPort"`
}

// Registry holds the set of known agents and supports lookup by
// name, skill, or tag.
type Registry struct {
	agents []AgentEntry
}

type registryFile struct {
	Agents []AgentEntry `json:"agents"`
}

// LoadFromFile reads a JSON registry file from disk.
func LoadFromFile(path string) (*Registry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("registry: open %s: %w", path, err)
	}
	defer f.Close()
	return LoadFromReader(f)
}

// LoadFromReader decodes, validates, and returns a Registry.
func LoadFromReader(r io.Reader) (*Registry, error) {
	var file registryFile
	if err := json.NewDecoder(r).Decode(&file); err != nil {
		return nil, fmt.Errorf("registry: decode JSON: %w", err)
	}

	if err := validate(file.Agents); err != nil {
		return nil, err
	}

	slog.Info("registry loaded", "agents", len(file.Agents))
	return &Registry{agents: file.Agents}, nil
}

func validate(entries []AgentEntry) error {
	names := make(map[string]bool)
	subjects := make(map[string]bool)

	for i, e := range entries {
		if e.Card.Name == "" {
			return fmt.Errorf("registry: agent[%d]: name is required", i)
		}
		if e.Card.Description == "" {
			return fmt.Errorf("registry: agent[%d] (%s): description is required", i, e.Card.Name)
		}
		if e.Card.Version == "" {
			return fmt.Errorf("registry: agent[%d] (%s): version is required", i, e.Card.Name)
		}
		if e.QueueSubject == "" {
			return fmt.Errorf("registry: agent[%d] (%s): queueSubject is required", i, e.Card.Name)
		}
		if e.Runtime == "" {
			return fmt.Errorf("registry: agent[%d] (%s): runtime is required", i, e.Card.Name)
		}
		if e.ACPPort <= 0 {
			return fmt.Errorf("registry: agent[%d] (%s): acpPort must be > 0", i, e.Card.Name)
		}
		if len(e.Card.Skills) == 0 {
			return fmt.Errorf("registry: agent[%d] (%s): at least one skill is required", i, e.Card.Name)
		}
		for j, s := range e.Card.Skills {
			if s.ID == "" {
				return fmt.Errorf("registry: agent[%d] (%s) skill[%d]: id is required", i, e.Card.Name, j)
			}
			if s.Name == "" {
				return fmt.Errorf("registry: agent[%d] (%s) skill[%d]: name is required", i, e.Card.Name, j)
			}
		}
		if names[e.Card.Name] {
			return fmt.Errorf("registry: duplicate agent name: %s", e.Card.Name)
		}
		names[e.Card.Name] = true

		if subjects[e.QueueSubject] {
			return fmt.Errorf("registry: duplicate queueSubject: %s", e.QueueSubject)
		}
		subjects[e.QueueSubject] = true
	}
	return nil
}

// FindBySkill returns all agents that declare the given skill ID.
func (reg *Registry) FindBySkill(skillID string) []AgentEntry {
	var result []AgentEntry
	for _, e := range reg.agents {
		for _, s := range e.Card.Skills {
			if s.ID == skillID {
				result = append(result, e)
				break
			}
		}
	}
	return result
}

// FindByTag returns all agents that have at least one skill with the given tag.
func (reg *Registry) FindByTag(tag string) []AgentEntry {
	var result []AgentEntry
	for _, e := range reg.agents {
		matched := false
		for _, s := range e.Card.Skills {
			if matched {
				break
			}
			for _, t := range s.Tags {
				if t == tag {
					result = append(result, e)
					matched = true
					break
				}
			}
		}
	}
	return result
}

// FindByName returns the agent entry with the given name, or false if not found.
func (reg *Registry) FindByName(name string) (*AgentEntry, bool) {
	for _, e := range reg.agents {
		if e.Card.Name == name {
			return &e, true
		}
	}
	return nil, false
}

// Route returns the NATS queue subject for the first agent that declares
// the given skill ID.
func (reg *Registry) Route(skillID string) (string, error) {
	matches := reg.FindBySkill(skillID)
	if len(matches) == 0 {
		return "", errors.New("registry: no agent found for skill: " + skillID)
	}
	return matches[0].QueueSubject, nil
}

// All returns a copy of every agent entry in the registry.
func (reg *Registry) All() []AgentEntry {
	out := make([]AgentEntry, len(reg.agents))
	copy(out, reg.agents)
	return out
}
