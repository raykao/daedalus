package registry

import "errors"

// RoutingRequest describes intent-based routing criteria.
type RoutingRequest struct {
	SkillID          string
	Tags             []string
	PreferredRuntime string
}

// Score evaluates how well an AgentEntry matches a RoutingRequest.
//
// Scoring rules:
//   - Exact skill ID match: +1.0
//   - Each matching tag (across all skills, counted once per unique tag): +0.5
//   - Preferred runtime match: +0.1
func Score(entry AgentEntry, request RoutingRequest) float64 {
	score := 0.0

	for _, s := range entry.Card.Skills {
		if request.SkillID != "" && s.ID == request.SkillID {
			score += 1.0
			break
		}
	}

	if len(request.Tags) > 0 {
		// Collect all unique tags from the agent's skills.
		agentTags := make(map[string]bool)
		for _, s := range entry.Card.Skills {
			for _, t := range s.Tags {
				agentTags[t] = true
			}
		}
		for _, t := range request.Tags {
			if agentTags[t] {
				score += 0.5
			}
		}
	}

	if request.PreferredRuntime != "" && entry.Runtime == request.PreferredRuntime {
		score += 0.1
	}

	return score
}

// RouteByScore returns the highest-scoring agent for the given request.
// Returns an error if no agent has a score greater than zero.
func (reg *Registry) RouteByScore(req RoutingRequest) (*AgentEntry, error) {
	var best *AgentEntry
	bestScore := 0.0

	for i := range reg.agents {
		s := Score(reg.agents[i], req)
		if s > bestScore {
			bestScore = s
			entry := reg.agents[i]
			best = &entry
		}
	}

	if best == nil {
		return nil, errors.New("registry: no agent matched the routing request")
	}
	return best, nil
}
