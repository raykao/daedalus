package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Handler dispatches JSON-RPC requests to the correct ACP method handler.
type Handler struct {
	srv *Server
}

func NewHandler(srv *Server) *Handler {
	return &Handler{srv: srv}
}

// Dispatch routes a request to the appropriate method handler.
func (h *Handler) Dispatch(ctx context.Context, c *conn, req *Request) {
	switch req.Method {
	case "initialize":
		h.handleInitialize(ctx, c, req)
	case "session/new":
		h.handleSessionNew(ctx, c, req)
	case "session/prompt":
		h.handleSessionPrompt(ctx, c, req)
	case "session/cancel":
		h.handleSessionCancel(ctx, c, req)
	case "session/load":
		h.handleSessionLoad(ctx, c, req)
	default:
		slog.Warn("unknown method", "method", req.Method)
		_ = c.WriteResponse(req.ID, nil, &RPCError{
			Code:    ErrMethodNotFound,
			Message: fmt.Sprintf("method not found: %s", req.Method),
		})
	}
}

// ── initialize ────────────────────────────────────────────────────────────────

type InitializeParams struct {
	ProtocolVersion int            `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

type InitializeResult struct {
	ProtocolVersion int                `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ComponentInfo      `json:"serverInfo"`
}

type ServerCapabilities struct {
	LoadSession       bool `json:"loadSession"`
	Streaming         bool `json:"streaming"`
	PermissionRequest bool `json:"permissionRequest"`
}

type ComponentInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func (h *Handler) handleInitialize(_ context.Context, c *conn, req *Request) {
	var params InitializeParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "invalid params"})
		return
	}
	slog.Info("initialize", "client", params.ClientInfo.Name, "version", params.ClientInfo.Version)

	result := InitializeResult{
		ProtocolVersion: 1,
		Capabilities: ServerCapabilities{
			LoadSession:       h.srv.cfg.LoadSessionSupport,
			Streaming:         true,
			PermissionRequest: true,
		},
		ServerInfo: ComponentInfo{Name: "mock-copilot", Version: "0.1.0"},
	}
	_ = c.WriteResponse(req.ID, result, nil)
}

// ── session/new ───────────────────────────────────────────────────────────────

type SessionNewParams struct {
	Cwd        string      `json:"cwd"`
	MCPServers []MCPServer `json:"mcpServers,omitempty"`
}

type MCPServer struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Command string `json:"command"`
}

func (h *Handler) handleSessionNew(_ context.Context, c *conn, req *Request) {
	var params SessionNewParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "invalid params"})
		return
	}

	sess := &Session{
		ID:      "sess-" + newUUID(),
		WorkDir: params.Cwd,
	}
	if !h.srv.addSession(sess) {
		_ = c.WriteResponse(req.ID, nil, &RPCError{
			Code:    ErrInternal,
			Message: "max sessions exceeded",
		})
		return
	}
	slog.Info("session/new", "sessionId", sess.ID, "cwd", params.Cwd, "mcpServers", len(params.MCPServers))
	_ = c.WriteResponse(req.ID, map[string]string{"sessionId": sess.ID}, nil)
}

// ── session/prompt ────────────────────────────────────────────────────────────

// ContentPart is a single content block in a session/prompt request.
type ContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type SessionPromptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []ContentPart `json:"prompt"`
}

// SessionUpdateNotificationParams is the params field of a session/update
// notification (the v1 streaming envelope).
type SessionUpdateNotificationParams struct {
	SessionID string            `json:"sessionId"`
	Update    SessionUpdateBody `json:"update"`
}

// SessionUpdateBody describes one streamed update in v1 wire format.
type SessionUpdateBody struct {
	SessionUpdate string              `json:"sessionUpdate"`
	Content       SessionUpdateContent `json:"content,omitempty"`
}

// SessionUpdateContent is the text content of a streamed update.
type SessionUpdateContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PermissionToolCall is the toolCall block of a session/request_permission.
type PermissionToolCall struct {
	Tool    string `json:"tool"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// PermissionOption describes one option presented to the client.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Label    string `json:"label"`
}

// PermissionRequestParams is the params of session/request_permission.
type PermissionRequestParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  PermissionToolCall `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// permissionApprovalResult is the typed shape of a session/request_permission
// response's `result` field.
type permissionApprovalResult struct {
	Outcome struct {
		OptionID string `json:"optionId"`
	} `json:"outcome"`
}

// unmarshalResult decodes a Response.Result (which may be json.RawMessage if
// it round-tripped via the read loop, or a Go value if set by the server)
// into the destination struct.
func unmarshalResult(result any, dst any) error {
	if result == nil {
		return fmt.Errorf("nil result")
	}
	if raw, ok := result.(json.RawMessage); ok {
		return json.Unmarshal(raw, dst)
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

type Artifact struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type PromptResult struct {
	SessionID string     `json:"sessionId"`
	Content   string     `json:"content"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}

func (h *Handler) handleSessionPrompt(ctx context.Context, c *conn, req *Request) {
	var params SessionPromptParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "invalid params"})
		return
	}

	sess, ok := h.srv.getSession(params.SessionID)
	if !ok {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "unknown sessionId"})
		return
	}

	if h.srv.cfg.FailOnPrompt {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInternal, Message: "prompt processing failed"})
		return
	}

	// Concatenate text parts for history storage.
	var promptTextBuilder strings.Builder
	for _, p := range params.Prompt {
		if p.Type == "text" {
			promptTextBuilder.WriteString(p.Text)
		}
	}
	promptText := promptTextBuilder.String()

	slog.Info("session/prompt", "sessionId", params.SessionID, "promptLen", len(promptText))

	// Stream agent_message_chunk updates via session/update notifications.
	chunks := []string{"I'll create ", "the file for you."}
	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return
		case <-time.After(h.srv.cfg.StreamingDelay):
		}
		_ = c.WriteNotification("session/update", SessionUpdateNotificationParams{
			SessionID: params.SessionID,
			Update: SessionUpdateBody{
				SessionUpdate: "agent_message_chunk",
				Content:       SessionUpdateContent{Type: "text", Text: chunk},
			},
		})
	}

	// Optionally issue a permission request (server→client request) and wait
	// for the client's response before completing the prompt.
	if h.srv.cfg.SendPermissions {
		permParams := PermissionRequestParams{
			SessionID: params.SessionID,
			ToolCall: PermissionToolCall{
				Tool:    "bash",
				Command: "echo 'hello world' > hello.txt",
				Reason:  "Creating hello.txt",
			},
			Options: []PermissionOption{
				{OptionID: "allow_once", Label: "Allow once"},
				{OptionID: "deny", Label: "Deny"},
			},
		}
		resp, err := c.WriteRequestAwaitResponse(ctx, "session/request_permission", permParams, 30*time.Second)
		if err != nil {
			slog.Warn("permission request failed", "err", err)
			_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInternal, Message: "permission request: " + err.Error()})
			return
		}
		if resp.Error != nil {
			slog.Warn("permission request returned error", "code", resp.Error.Code, "msg", resp.Error.Message)
			_ = c.WriteResponse(req.ID, nil, resp.Error)
			return
		}

		// Parse the outcome and validate optionId.
		var approval permissionApprovalResult
		if err := unmarshalResult(resp.Result, &approval); err != nil || approval.Outcome.OptionID == "" {
			slog.Warn("invalid permission response", "err", err)
			_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInternal, Message: "invalid permission response"})
			return
		}
		if approval.Outcome.OptionID != "allow_once" {
			slog.Info("permission denied", "sessionId", params.SessionID, "optionId", approval.Outcome.OptionID)
			_ = c.WriteResponse(req.ID, nil, &RPCError{
				Code:    ErrInternal,
				Message: "permission denied: " + approval.Outcome.OptionID,
			})
			return
		}
		slog.Info("permission granted", "sessionId", params.SessionID, "optionId", approval.Outcome.OptionID)
	}

	// Final response after optional delay.
	select {
	case <-ctx.Done():
		return
	case <-time.After(h.srv.cfg.ResponseDelay):
	}

	finalContent := "I've created hello.txt with the contents 'hello world'."
	sess.AppendHistory(promptText, finalContent)

	result := PromptResult{
		SessionID: params.SessionID,
		Content:   finalContent,
		Artifacts: []Artifact{{Path: "hello.txt", Content: "hello world"}},
	}
	_ = c.WriteResponse(req.ID, result, nil)
}

// ── session/cancel ────────────────────────────────────────────────────────────

type SessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

func (h *Handler) handleSessionCancel(_ context.Context, c *conn, req *Request) {
	var params SessionCancelParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "invalid params"})
		return
	}
	_, ok := h.srv.getSession(params.SessionID)
	if !ok {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "unknown sessionId"})
		return
	}
	slog.Info("session/cancel", "sessionId", params.SessionID)
	_ = c.WriteResponse(req.ID, map[string]any{"sessionId": params.SessionID, "canceled": true}, nil)
}

// ── session/load ──────────────────────────────────────────────────────────────

type SessionLoadParams struct {
	SessionID string `json:"sessionId"`
}

type SessionUpdateParams struct {
	SessionID string       `json:"sessionId"`
	History   []HistoryEntry `json:"history"`
}

func (h *Handler) handleSessionLoad(_ context.Context, c *conn, req *Request) {
	var params SessionLoadParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "invalid params"})
		return
	}
	sess, ok := h.srv.getSession(params.SessionID)
	if !ok {
		_ = c.WriteResponse(req.ID, nil, &RPCError{Code: ErrInvalidParams, Message: "unknown sessionId"})
		return
	}
	slog.Info("session/load", "sessionId", params.SessionID)

	// Replay history via notification.
	sess.mu.Lock()
	history := make([]HistoryEntry, len(sess.History))
	copy(history, sess.History)
	sess.mu.Unlock()

	if len(history) > 0 {
		_ = c.WriteNotification("session/update", SessionUpdateParams{
			SessionID: params.SessionID,
			History:   history,
		})
	}
	_ = c.WriteResponse(req.ID, map[string]any{"sessionId": params.SessionID, "loaded": true}, nil)
}

// newUUID returns a random 16-byte hex string (no external dependencies).
func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
