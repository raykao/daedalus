# Agent Registry

The agent registry is a static JSON file that maps agent capabilities to infrastructure routing information. It enables the orchestrator to discover agents and route tasks based on skill matching, tag search, or scored ranking.

## Registry JSON Format

The registry file (`deploy/config/agent-registry.json`) contains a single top-level object with an `agents` array:

```json
{
  "agents": [
    {
      "card": { ... },
      "queueSubject": "agent.tasks.<name>",
      "runtime": "<runtime-type>",
      "acpPort": 3000
    }
  ]
}
```

### AgentEntry fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `card` | AgentCard | Yes | A2A agent card describing the agent |
| `queueSubject` | string | Yes | NATS subject the orchestrator publishes tasks to |
| `runtime` | string | Yes | Runtime type (e.g. `copilot-bridge`, `acp-container`) |
| `acpPort` | int | Yes | Port the agent listens on (must be > 0) |

### AgentCard fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique agent identifier |
| `description` | string | Yes | Human-readable description |
| `version` | string | Yes | Semantic version |
| `defaultInputModes` | []string | No | Supported input modes (e.g. `["text"]`) |
| `defaultOutputModes` | []string | No | Supported output modes |
| `skills` | []AgentSkill | Yes | At least one skill required |
| `capabilities` | AgentCapabilities | No | Protocol capabilities |

### AgentSkill fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique skill identifier (e.g. `code-review`) |
| `name` | string | Yes | Human-readable skill name |
| `description` | string | No | What the skill does |
| `tags` | []string | No | Searchable tags for tag-based routing |
| `examples` | []string | No | Example prompts |

## Routing

### Exact skill match (`Route`)

The simplest routing path: given a skill ID, find the first agent that declares it and return the NATS queue subject.

```
skill ID --> FindBySkill --> first match --> queueSubject
```

If no agent declares the skill, `Route` returns an error.

### Scored routing (`RouteByScore`)

For more nuanced routing, `RouteByScore` evaluates every agent against a `RoutingRequest` and returns the highest-scoring entry.

#### Scoring algorithm

| Criterion | Points | Notes |
|-----------|--------|-------|
| Exact skill ID match | +1.0 | Checked once across all agent skills |
| Tag match | +0.5 per tag | Each requested tag matched against the union of all skill tags |
| Preferred runtime | +0.1 | Bonus when `entry.Runtime == request.PreferredRuntime` |

Scores accumulate. The agent with the highest total score wins. If no agent scores above zero, `RouteByScore` returns an error.

### Lookup helpers

- `FindBySkill(skillID)`: returns all agents declaring the skill
- `FindByTag(tag)`: returns all agents with at least one skill carrying the tag
- `FindByName(name)`: exact name lookup
- `All()`: returns every registered agent

## Adding a New Agent

1. Define the agent's skills, tags, and capabilities.
2. Add a new entry to `deploy/config/agent-registry.json`:

```json
{
  "card": {
    "name": "my-agent",
    "description": "My custom agent",
    "version": "1.0.0",
    "defaultInputModes": ["text"],
    "defaultOutputModes": ["text"],
    "skills": [
      {
        "id": "my-skill",
        "name": "My Skill",
        "description": "Does something useful",
        "tags": ["custom", "example"]
      }
    ],
    "capabilities": {}
  },
  "queueSubject": "agent.tasks.my-agent",
  "runtime": "acp-container",
  "acpPort": 3003
}
```

3. Ensure the `name` and `queueSubject` are unique across the registry.
4. Restart the orchestrator (or reload the registry) for the new agent to become routable.

### Validation rules

The registry loader rejects files that violate any of these constraints:

- Duplicate agent names
- Duplicate queue subjects
- Missing required fields (name, description, version, queueSubject, runtime)
- `acpPort` must be greater than zero
- Each agent must have at least one skill
- Each skill must have a non-empty `id` and `name`

## Future Work

- **Dynamic registration via NATS**: agents announce themselves on a well-known subject; the registry updates in real time.
- **AgentCard discovery endpoint**: each agent serves `/.well-known/agent.json` per the A2A spec; the orchestrator periodically fetches and reconciles.
- **Health-aware routing**: integrate liveness checks so unhealthy agents are excluded from routing decisions.
- **Weighted scoring**: allow registry entries to carry weight multipliers for priority-based routing.
