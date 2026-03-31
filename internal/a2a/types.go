package a2a

import "encoding/json"

// TaskState - lifecycle of an A2A task
type TaskState string

const (
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCanceled      TaskState = "canceled"
	TaskStateInputRequired TaskState = "input-required"
	TaskStateRejected      TaskState = "rejected"
)

// Part - content container (text, file, data)
type Part struct {
	Text      string          `json:"text,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

// Message - one unit of communication
type Message struct {
	MessageID string         `json:"messageId"`
	ContextID string         `json:"contextId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Role      string         `json:"role"`
	Parts     []Part         `json:"parts"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// TaskStatus
type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp string    `json:"timestamp,omitempty"`
}

// Task - core unit
type Task struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	History   []Message      `json:"history,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Artifact - task output
type Artifact struct {
	ArtifactID  string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// SendMessageRequest - what the orchestrator publishes to NATS
type SendMessageRequest struct {
	Message       Message                   `json:"message"`
	Configuration *SendMessageConfiguration `json:"configuration,omitempty"`
}

// SendMessageConfiguration - optional config for message handling
type SendMessageConfiguration struct {
	AcceptedOutputModes []string `json:"acceptedOutputModes,omitempty"`
	HistoryLength       *int     `json:"historyLength,omitempty"`
}

// AgentCard - describes agent capabilities
type AgentCard struct {
	Name               string            `json:"name"`
	Description        string            `json:"description"`
	Version            string            `json:"version"`
	DefaultInputModes  []string          `json:"defaultInputModes"`
	DefaultOutputModes []string          `json:"defaultOutputModes"`
	Skills             []AgentSkill      `json:"skills"`
	Capabilities       AgentCapabilities `json:"capabilities,omitempty"`
}

// AgentSkill - a capability the agent can perform
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples,omitempty"`
}

// AgentCapabilities - agent protocol capabilities
type AgentCapabilities struct {
	Streaming         bool `json:"streaming,omitempty"`
	PushNotifications bool `json:"pushNotifications,omitempty"`
}

// ExtractPromptText extracts concatenated text from message parts
func (m *Message) ExtractPromptText() string {
	var result string
	for _, part := range m.Parts {
		if part.Text != "" {
			result += part.Text
		}
	}
	return result
}
