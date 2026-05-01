package acp

import "encoding/json"

// Request is a JSON-RPC 2.0 request message
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response message
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"` // present on notifications
	Params  json.RawMessage `json:"params,omitempty"` // present on notifications
}

// RPCError is a JSON-RPC 2.0 error object
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return e.Message
}

// IsNotification returns true if the message has no ID (it's a server-sent notification)
func (r *Response) IsNotification() bool {
	return r.ID == nil && r.Method != ""
}

// IsServerRequest returns true if the message is a server-to-client request
// (has both a method and a non-nil ID). These require a JSON-RPC response.
func (r *Response) IsServerRequest() bool {
	return r.Method != "" && r.ID != nil
}

// InitializeParams for the initialize method
type InitializeParams struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// ClientCapabilities sent during initialize
type ClientCapabilities struct {
	Streaming bool `json:"streaming"`
}

// ClientInfo identifies the client
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult from the initialize method
type InitializeResult struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
}

// ServerCapabilities advertised by the agent
type ServerCapabilities struct {
	LoadSession bool `json:"loadSession,omitempty"`
	Streaming   bool `json:"streaming,omitempty"`
}

// ServerInfo identifies the agent server
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SessionNewParams for session/new
type SessionNewParams struct {
	WorkDir    string        `json:"cwd"`
	MCPServers []interface{} `json:"mcpServers"`
}

// SessionNewResult from session/new
type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// PromptPart is a single content part in a session/prompt request
type PromptPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// SessionPromptParams for session/prompt
type SessionPromptParams struct {
	SessionID string       `json:"sessionId"`
	Prompt    []PromptPart `json:"prompt"`
}

// SessionPromptResult from session/prompt
type SessionPromptResult struct {
	SessionID string        `json:"sessionId"`
	Content   string        `json:"content"`
	Artifacts []interface{} `json:"artifacts,omitempty"`
}

// MessageDeltaParams from assistant.message_delta notifications
type MessageDeltaParams struct {
	SessionID string `json:"sessionId"`
	Content   string `json:"content"`
}

// PermissionRequestParams from session/request_permission server requests
type PermissionRequestParams struct {
	SessionID string          `json:"sessionId"`
	ToolCall  json.RawMessage `json:"toolCall,omitempty"`
	Options   json.RawMessage `json:"options,omitempty"`
}

// PermissionApprovalResult is the JSON-RPC result for session/request_permission
type PermissionApprovalResult struct {
	Outcome PermissionOutcome `json:"outcome"`
}

// PermissionOutcome contains the selected option
type PermissionOutcome struct {
	OptionID string `json:"optionId"`
}

// SessionUpdateParams from session/update notifications
type SessionUpdateParams struct {
	SessionID string            `json:"sessionId"`
	Update    SessionUpdateBody `json:"update"`
}

// SessionUpdateBody is the update content within a session/update notification
type SessionUpdateBody struct {
	SessionUpdate string               `json:"sessionUpdate"` // "agent_message_chunk", "agent_thought_chunk", "tool_call", "tool_call_update"
	Content       SessionUpdateContent `json:"content,omitempty"`
}

// SessionUpdateContent is the text content of a session update
type SessionUpdateContent struct {
	Type string `json:"type,omitempty"` // "text"
	Text string `json:"text,omitempty"`
}

// SessionCancelParams for session/cancel
type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}
