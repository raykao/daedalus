package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
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
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"clientInfo"`
}

type InitializeResult struct {
	ProtocolVersion string                `json:"protocolVersion"`
	Capabilities    ServerCapabilities    `json:"capabilities"`
	ServerInfo      ComponentInfo         `json:"serverInfo"`
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
		ProtocolVersion: "2025-01-01",
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
	WorkDir    string      `json:"workDir"`
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
		WorkDir: params.WorkDir,
	}
	if !h.srv.addSession(sess) {
		_ = c.WriteResponse(req.ID, nil, &RPCError{
			Code:    ErrInternal,
			Message: "max sessions exceeded",
		})
		return
	}
	slog.Info("session/new", "sessionId", sess.ID, "workDir", params.WorkDir, "mcpServers", len(params.MCPServers))
	_ = c.WriteResponse(req.ID, map[string]string{"sessionId": sess.ID}, nil)
}

// ── session/prompt ────────────────────────────────────────────────────────────

type SessionPromptParams struct {
	SessionID string `json:"sessionId"`
	Prompt    string `json:"prompt"`
}

type MessageDeltaParams struct {
	SessionID string `json:"sessionId"`
	Content   string `json:"content"`
}

type PermissionRequestParams struct {
	SessionID string `json:"sessionId"`
	Tool      string `json:"tool"`
	Command   string `json:"command"`
	Reason    string `json:"reason"`
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

	slog.Info("session/prompt", "sessionId", params.SessionID)

	// Stream assistant.message_delta chunks.
	chunks := []string{"I'll create ", "the file for you."}
	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return
		case <-time.After(h.srv.cfg.StreamingDelay):
		}
		_ = c.WriteNotification("assistant.message_delta", MessageDeltaParams{
			SessionID: params.SessionID,
			Content:   chunk,
		})
	}

	// Optionally emit a permission request.
	if h.srv.cfg.SendPermissions {
		_ = c.WriteNotification("session/request_permission", PermissionRequestParams{
			SessionID: params.SessionID,
			Tool:      "bash",
			Command:   "echo 'hello world' > hello.txt",
			Reason:    "Creating hello.txt",
		})
	}

	// Final response after optional delay.
	select {
	case <-ctx.Done():
		return
	case <-time.After(h.srv.cfg.ResponseDelay):
	}

	finalContent := "I've created hello.txt with the contents 'hello world'."
	sess.AppendHistory(params.Prompt, finalContent)

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
