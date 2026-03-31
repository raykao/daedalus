package registry

// AgentRegistry defines the common interface for agent discovery.
// Both static (Registry) and dynamic (DynamicRegistry) implementations
// satisfy this interface.
type AgentRegistry interface {
	FindBySkill(skillID string) []AgentEntry
	FindByTag(tag string) []AgentEntry
	FindByName(name string) (*AgentEntry, bool)
	Route(skillID string) (string, error)
	RouteByScore(req RoutingRequest) (*AgentEntry, error)
	All() []AgentEntry
}

// compile-time interface assertions
var _ AgentRegistry = (*Registry)(nil)
var _ AgentRegistry = (*DynamicRegistry)(nil)
