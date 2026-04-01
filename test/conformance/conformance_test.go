package conformance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raykao/daedalus/internal/a2a"
)

// newTestServer creates an httptest.Server running the reference A2A handler.
func newTestServer(t *testing.T) (*httptest.Server, ConformanceConfig) {
	t.Helper()
	srv := httptest.NewServer(newReferenceHandler())
	t.Cleanup(srv.Close)
	cfg := ConformanceConfig{
		BaseURL: srv.URL,
		Timeout: 5 * time.Second,
		SkipSSE: true,
	}
	return srv, cfg
}

func TestHealthEndpoint(t *testing.T) {
	_, cfg := newTestServer(t)

	tests := []struct {
		name       string
		path       string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "returns 200 with ok status",
			path:       "/health",
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Get(cfg.BaseURL + tt.path)
			if err != nil {
				t.Fatalf("GET %s: %v", tt.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			ct := resp.Header.Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if body["status"] != "ok" {
				t.Errorf("status = %q, want \"ok\"", body["status"])
			}
		})
	}
}

func TestHealthEndpointWaitForHealthy(t *testing.T) {
	_, cfg := newTestServer(t)

	if err := waitForHealthy(cfg, 2*time.Second); err != nil {
		t.Fatalf("waitForHealthy: %v", err)
	}
}

func TestAgentCardEndpoint(t *testing.T) {
	_, cfg := newTestServer(t)

	tests := []struct {
		name    string
		check   func(t *testing.T, card *a2a.AgentCard)
	}{
		{
			name: "returns valid AgentCard with required fields",
			check: func(t *testing.T, card *a2a.AgentCard) {
				t.Helper()
				if card.Name == "" {
					t.Error("AgentCard.Name is empty")
				}
				if card.Description == "" {
					t.Error("AgentCard.Description is empty")
				}
				if card.Version == "" {
					t.Error("AgentCard.Version is empty")
				}
				if len(card.Skills) == 0 {
					t.Error("AgentCard.Skills is empty, at least one skill required")
				}
			},
		},
		{
			name: "skills have required fields",
			check: func(t *testing.T, card *a2a.AgentCard) {
				t.Helper()
				for i, skill := range card.Skills {
					if skill.ID == "" {
						t.Errorf("skill[%d].ID is empty", i)
					}
					if skill.Name == "" {
						t.Errorf("skill[%d].Name is empty", i)
					}
					if skill.Description == "" {
						t.Errorf("skill[%d].Description is empty", i)
					}
					if skill.Tags == nil {
						t.Errorf("skill[%d].Tags is nil", i)
					}
				}
			},
		},
		{
			name: "validates against agent-card JSON schema",
			check: func(t *testing.T, card *a2a.AgentCard) {
				t.Helper()
				if err := validateAgentCard(card); err != nil {
					t.Errorf("AgentCard schema validation failed: %v", err)
				}
			},
		},
	}

	card, err := getAgentCard(cfg)
	if err != nil {
		t.Fatalf("getAgentCard: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, card)
		})
	}
}

func TestAgentCardContentType(t *testing.T) {
	_, cfg := newTestServer(t)

	resp, err := http.Get(cfg.BaseURL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("GET agent card: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestTaskExecution(t *testing.T) {
	_, cfg := newTestServer(t)

	tests := []struct {
		name      string
		req       a2a.SendMessageRequest
		wantState a2a.TaskState
		wantID    string
		checkFn   func(t *testing.T, task *a2a.Task)
	}{
		{
			name: "completed task with echo response",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-001",
					TaskID:    "task-001",
					Role:      "user",
					Parts:     []a2a.Part{{Text: "hello world"}},
				},
			},
			wantState: a2a.TaskStateCompleted,
			wantID:    "task-001",
			checkFn: func(t *testing.T, task *a2a.Task) {
				t.Helper()
				if len(task.Artifacts) == 0 {
					t.Fatal("completed task has no artifacts")
				}
				if task.Artifacts[0].Parts[0].Text == "" {
					t.Error("artifact has empty text")
				}
			},
		},
		{
			name: "task ID matches request taskId",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-002",
					TaskID:    "custom-task-id",
					Role:      "user",
					Parts:     []a2a.Part{{Text: "test"}},
				},
			},
			wantState: a2a.TaskStateCompleted,
			wantID:    "custom-task-id",
		},
		{
			name: "falls back to messageId when taskId is empty",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "fallback-msg-id",
					Role:      "user",
					Parts:     []a2a.Part{{Text: "test"}},
				},
			},
			wantState: a2a.TaskStateCompleted,
			wantID:    "fallback-msg-id",
		},
		{
			name: "failed task when no text in parts",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-empty",
					TaskID:    "task-empty",
					Role:      "user",
					Parts:     []a2a.Part{{Data: []byte(`{"key":"value"}`)}},
				},
			},
			wantState: a2a.TaskStateFailed,
			wantID:    "task-empty",
			checkFn: func(t *testing.T, task *a2a.Task) {
				t.Helper()
				if task.Status.Message == nil {
					t.Fatal("failed task has no status.message")
				}
			},
		},
		{
			name: "validates task against schema",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-schema",
					TaskID:    "task-schema",
					Role:      "user",
					Parts:     []a2a.Part{{Text: "validate me"}},
				},
			},
			wantState: a2a.TaskStateCompleted,
			wantID:    "task-schema",
			checkFn: func(t *testing.T, task *a2a.Task) {
				t.Helper()
				if err := validateTask(task); err != nil {
					t.Errorf("task schema validation failed: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, err := sendMessage(cfg, tt.req)
			if err != nil {
				t.Fatalf("sendMessage: %v", err)
			}

			if task.ID != tt.wantID {
				t.Errorf("task.ID = %q, want %q", task.ID, tt.wantID)
			}

			if task.Status.State != tt.wantState {
				t.Errorf("task.Status.State = %q, want %q", task.Status.State, tt.wantState)
			}

			if tt.checkFn != nil {
				tt.checkFn(t, task)
			}
		})
	}
}

func TestTaskExecutionEdgeCases(t *testing.T) {
	_, cfg := newTestServer(t)

	tests := []struct {
		name      string
		req       a2a.SendMessageRequest
		wantState a2a.TaskState
		wantID    string
	}{
		{
			name: "empty parts array treated as no text",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-empty-parts",
					TaskID:    "task-empty-parts",
					Role:      "user",
					Parts:     []a2a.Part{},
				},
			},
			wantState: a2a.TaskStateFailed,
			wantID:    "task-empty-parts",
		},
		{
			name: "very long prompt text",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-long",
					TaskID:    "task-long",
					Role:      "user",
					Parts:     []a2a.Part{{Text: strings.Repeat("a", 100000)}},
				},
			},
			wantState: a2a.TaskStateCompleted,
			wantID:    "task-long",
		},
		{
			name: "missing taskId uses messageId",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "only-msg-id",
					Role:      "user",
					Parts:     []a2a.Part{{Text: "test"}},
				},
			},
			wantState: a2a.TaskStateCompleted,
			wantID:    "only-msg-id",
		},
		{
			name: "both taskId and messageId empty generates fallback ID",
			req: a2a.SendMessageRequest{
				Message: a2a.Message{
					Role:  "user",
					Parts: []a2a.Part{{Text: "test"}},
				},
			},
			wantState: a2a.TaskStateCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, err := sendMessage(cfg, tt.req)
			if err != nil {
				t.Fatalf("sendMessage: %v", err)
			}

			if tt.wantID != "" {
				if task.ID != tt.wantID {
					t.Errorf("task.ID = %q, want %q", task.ID, tt.wantID)
				}
			} else {
				if task.ID == "" {
					t.Error("task.ID is empty, want non-empty fallback ID")
				}
				if !strings.HasPrefix(task.ID, "anonymous-") {
					t.Errorf("task.ID = %q, want prefix \"anonymous-\"", task.ID)
				}
			}

			if task.Status.State != tt.wantState {
				t.Errorf("task.Status.State = %q, want %q", task.Status.State, tt.wantState)
			}
		})
	}
}

func TestUnknownFieldsTolerated(t *testing.T) {
	_, cfg := newTestServer(t)

	// Send a request with extra unknown fields — the server should ignore them
	body := `{
		"message": {
			"messageId": "msg-compat",
			"taskId": "task-compat",
			"role": "user",
			"parts": [{"text": "hello"}],
			"futureField": "should be ignored"
		},
		"unknownTopLevel": true
	}`

	resp, err := http.Post(cfg.BaseURL+"/", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var task a2a.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatalf("decoding task: %v", err)
	}

	if task.ID != "task-compat" {
		t.Errorf("task.ID = %q, want \"task-compat\"", task.ID)
	}
}

func TestContentTypes(t *testing.T) {
	_, cfg := newTestServer(t)

	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		ct         string
		wantStatus int
		wantCT     string
	}{
		{
			name:       "POST / rejects non-JSON content type",
			method:     http.MethodPost,
			path:       "/",
			body:       "not json",
			ct:         "text/plain",
			wantStatus: http.StatusUnsupportedMediaType,
		},
		{
			name:       "GET /health returns application/json",
			method:     http.MethodGet,
			path:       "/health",
			wantStatus: http.StatusOK,
			wantCT:     "application/json",
		},
		{
			name:       "GET agent card returns application/json",
			method:     http.MethodGet,
			path:       "/.well-known/agent-card.json",
			wantStatus: http.StatusOK,
			wantCT:     "application/json",
		},
		{
			name:       "POST / returns 400 for malformed JSON",
			method:     http.MethodPost,
			path:       "/",
			body:       `{not valid json`,
			ct:         "application/json",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "POST / accepts JSON with charset parameter",
			method:     http.MethodPost,
			path:       "/",
			body:       `{"message":{"messageId":"ct-test","role":"user","parts":[{"text":"test"}]}}`,
			ct:         "application/json; charset=utf-8",
			wantStatus: http.StatusOK,
			wantCT:     "application/json",
		},
	}

	client := &http.Client{Timeout: cfg.Timeout}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			var err error

			if tt.body != "" {
				req, err = http.NewRequest(tt.method, cfg.BaseURL+tt.path, strings.NewReader(tt.body))
			} else {
				req, err = http.NewRequest(tt.method, cfg.BaseURL+tt.path, nil)
			}
			if err != nil {
				t.Fatalf("creating request: %v", err)
			}

			if tt.ct != "" {
				req.Header.Set("Content-Type", tt.ct)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tt.method, tt.path, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			if tt.wantCT != "" {
				ct := resp.Header.Get("Content-Type")
				if !strings.HasPrefix(ct, tt.wantCT) {
					t.Errorf("Content-Type = %q, want prefix %q", ct, tt.wantCT)
				}
			}
		})
	}
}

func TestContractManifest(t *testing.T) {
	schema, err := loadSchema("runtime-contract.schema.json")
	if err != nil {
		t.Fatalf("loading runtime-contract schema: %v", err)
	}

	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name: "valid Level 1 manifest",
			input: mustMarshal(map[string]any{
				"contractVersion": "v1",
				"runtime": map[string]any{
					"name":    "test-agent",
					"version": "1.0.0",
				},
				"server": map[string]any{
					"port":          8080,
					"healthPath":    "/health",
					"agentCardPath": "/.well-known/agent-card.json",
					"taskPath":      "/",
				},
				"capabilities": map[string]any{
					"streaming":       false,
					"traceContext":    false,
					"gracefulShutdown": false,
				},
				"conformanceLevel": 1,
			}),
			wantErr: false,
		},
		{
			name: "valid Level 2 manifest with description",
			input: mustMarshal(map[string]any{
				"contractVersion": "v1",
				"runtime": map[string]any{
					"name":        "my-agent",
					"version":     "2.0.0",
					"description": "A production agent",
				},
				"server": map[string]any{
					"port":          9090,
					"healthPath":    "/health",
					"agentCardPath": "/.well-known/agent-card.json",
					"taskPath":      "/",
				},
				"capabilities": map[string]any{
					"streaming":          false,
					"traceContext":       true,
					"gracefulShutdown":   true,
					"contextCompaction":  false,
				},
				"conformanceLevel": 2,
			}),
			wantErr: false,
		},
		{
			name: "valid Level 3 manifest with all capabilities",
			input: mustMarshal(map[string]any{
				"contractVersion": "v1",
				"runtime": map[string]any{
					"name":    "full-agent",
					"version": "3.0.0",
				},
				"server": map[string]any{
					"port":          8080,
					"healthPath":    "/health",
					"agentCardPath": "/.well-known/agent-card.json",
					"taskPath":      "/",
				},
				"capabilities": map[string]any{
					"streaming":          true,
					"traceContext":       true,
					"gracefulShutdown":   true,
					"contextCompaction":  true,
				},
				"conformanceLevel": 3,
			}),
			wantErr: false,
		},
		{
			name:    "invalid missing contractVersion",
			input:   []byte(`{"runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid missing runtime",
			input:   []byte(`{"contractVersion":"v1","server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid missing server",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid missing capabilities",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid missing conformanceLevel",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false}}`),
			wantErr: true,
		},
		{
			name:    "invalid contractVersion value",
			input:   []byte(`{"contractVersion":"v2","runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid conformanceLevel too high",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":4}`),
			wantErr: true,
		},
		{
			name:    "invalid conformanceLevel zero",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":0}`),
			wantErr: true,
		},
		{
			name:    "invalid port too high",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"server":{"port":70000,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid empty runtime name",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid missing runtime version",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid missing server healthPath",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"streaming":false,"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
		{
			name:    "invalid missing capabilities streaming",
			input:   []byte(`{"contractVersion":"v1","runtime":{"name":"x","version":"1.0"},"server":{"port":8080,"healthPath":"/health","agentCardPath":"/.well-known/agent-card.json","taskPath":"/"},"capabilities":{"traceContext":false,"gracefulShutdown":false},"conformanceLevel":1}`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJSON(schema, tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateJSON() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
