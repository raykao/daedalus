package main

import (
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/raykao/daedalus/internal/a2a"
)

// echoAgentCard returns the AgentCard for the echo A2A server.
func echoAgentCard() a2a.AgentCard {
	return a2a.AgentCard{
		Name:               "echo-agent",
		Description:        "A minimal A2A echo agent for multi-runtime testing",
		Version:            "1.0.0",
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{
			{
				ID:          "echo",
				Name:        "Echo",
				Description: "Echoes input back with a prefix",
				Tags:        []string{"testing", "echo"},
			},
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming:         false,
			PushNotifications: false,
		},
	}
}

// newEchoHandler returns an http.Handler implementing a minimal A2A echo server.
// It supports health checks, agent card discovery, and task processing.
func newEchoHandler() http.Handler {
	mux := http.NewServeMux()

	card := echoAgentCard()
	cardJSON, _ := json.Marshal(card)

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cardJSON)
	})

	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		mediaType, _, _ := mime.ParseMediaType(ct)
		if mediaType != "application/json" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnsupportedMediaType)
			w.Write([]byte(`{"error":"unsupported content type"}`))
			return
		}

		var req a2a.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			errResp, _ := json.Marshal(map[string]string{"error": "invalid request body: " + err.Error()})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			w.Write(errResp)
			return
		}

		// Determine task ID: prefer taskId, fall back to messageId, then generate.
		taskID := req.Message.TaskID
		if taskID == "" {
			taskID = req.Message.MessageID
		}
		if taskID == "" {
			taskID = fmt.Sprintf("echo-%d", time.Now().UnixNano())
		}

		promptText := req.Message.ExtractPromptText()
		if promptText == "" {
			task := a2a.Task{
				ID: taskID,
				Status: a2a.TaskStatus{
					State: a2a.TaskStateFailed,
					Message: &a2a.Message{
						MessageID: "fail-" + taskID,
						Role:      "agent",
						Parts:     []a2a.Part{{Text: "no text content in message parts"}},
					},
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(task)
			return
		}

		task := a2a.Task{
			ID:     taskID,
			Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
			Artifacts: []a2a.Artifact{
				{
					ArtifactID: "output-1",
					Name:       "echo-output",
					Parts:      []a2a.Part{{Text: "echo: " + promptText}},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(task)
	})

	return mux
}
