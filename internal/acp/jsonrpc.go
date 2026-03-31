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

// InitializeParams for the initialize method
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
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
	ProtocolVersion string             `json:"protocolVersion"`
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
	WorkDir    string        `json:"workDir"`
	MCPServers []interface{} `json:"mcpServers"`
}

// SessionNewResult from session/new
type SessionNewResult struct {
	SessionID string `json:"sessionId"`
}

// SessionPromptParams for session/prompt
type SessionPromptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
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

// PermissionRequestParams from session/request_permission notifications
type PermissionRequestParams struct {
	SessionID string   `json:"sessionId"`
	Tool      string   `json:"tool"`
	Args      []string `json:"args"`
	Reason    string   `json:"reason"`
}

// PermissionResponseParams for approving a permission request
type PermissionResponseParams struct {
	SessionID string `json:"sessionId"`
	Approved  bool   `json:"approved"`
}

// SessionCancelParams for session/cancel
type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}
