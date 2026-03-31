// Package dispatch handles publishing TaskSpec messages to the correct NATS
// subjects via registry-based routing.
package dispatch

// TaskSpec defines a single task to dispatch to a worker agent.
// Uses structured input (explicit skill/prompt pairs) rather than
// LLM-based prompt splitting - deterministic and testable.
type TaskSpec struct {
	ID       string         // Unique task ID (generated if empty)
	SkillID  string         // Target skill for registry routing
	Tags     []string       // Optional tags for routing refinement
	Prompt   string         // The actual prompt to send to the agent
	Metadata map[string]any // Optional metadata passed through to A2A Message
	Priority int            // Reserved for future use; currently ignored by Dispatch
}
