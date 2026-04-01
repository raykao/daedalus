package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/raykao/agent-forge/internal/a2a"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ConformanceConfig holds the target server configuration for conformance tests.
type ConformanceConfig struct {
	BaseURL string
	Timeout time.Duration
	SkipSSE bool
}

// sendMessage sends a SendMessageRequest to the target server's task endpoint.
func sendMessage(cfg ConformanceConfig, req a2a.SendMessageRequest) (*a2a.Task, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, cfg.BaseURL+"/", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var task a2a.Task
	if err := json.Unmarshal(respBody, &task); err != nil {
		return nil, fmt.Errorf("decoding task response: %w", err)
	}
	return &task, nil
}

// getAgentCard retrieves the AgentCard from the target server's discovery endpoint.
func getAgentCard(cfg ConformanceConfig) (*a2a.AgentCard, error) {
	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Get(cfg.BaseURL + "/.well-known/agent-card.json")
	if err != nil {
		return nil, fmt.Errorf("requesting agent card: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading agent card response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var card a2a.AgentCard
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("decoding agent card: %w", err)
	}
	return &card, nil
}

// checkHealth performs a single health check against the target server.
func checkHealth(cfg ConformanceConfig) error {
	client := &http.Client{Timeout: cfg.Timeout}
	resp, err := client.Get(cfg.BaseURL + "/health")
	if err != nil {
		return fmt.Errorf("health check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", resp.StatusCode)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decoding health response: %w", err)
	}

	if result["status"] != "ok" {
		return fmt.Errorf("health status is %q, want \"ok\"", result["status"])
	}
	return nil
}

// waitForHealthy polls the health endpoint until it returns 200 or the timeout expires.
func waitForHealthy(cfg ConformanceConfig, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	interval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		if err := checkHealth(cfg); err == nil {
			return nil
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("server not healthy after %v", timeout)
}

// schemasDir returns the path to the JSON Schema files, relative to the test working directory.
const schemasRelPath = "../contract/schemas"

// newSchemaCompiler creates a JSON Schema compiler pre-loaded with all schemas.
func newSchemaCompiler() (*jsonschema.Compiler, error) {
	c := jsonschema.NewCompiler()

	entries, err := os.ReadDir(schemasRelPath)
	if err != nil {
		return nil, fmt.Errorf("reading schemas dir: %w", err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(schemasRelPath, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading schema %s: %w", e.Name(), err)
		}

		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("parsing schema %s: %w", e.Name(), err)
		}

		obj, ok := doc.(map[string]any)
		if !ok {
			continue
		}

		id, _ := obj["$id"].(string)
		if id == "" {
			continue
		}

		if err := c.AddResource(id, doc); err != nil {
			return nil, fmt.Errorf("adding schema resource %s: %w", id, err)
		}
	}

	return c, nil
}

// loadSchema compiles a JSON Schema by filename from the schemas directory.
func loadSchema(name string) (*jsonschema.Schema, error) {
	c, err := newSchemaCompiler()
	if err != nil {
		return nil, err
	}
	id := "https://agent-forge/schemas/" + name
	return c.Compile(id)
}

// validateAgentCard validates an AgentCard against agent-card.schema.json.
func validateAgentCard(card *a2a.AgentCard) error {
	schema, err := loadSchema("agent-card.schema.json")
	if err != nil {
		return fmt.Errorf("loading agent-card schema: %w", err)
	}

	data, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshaling agent card: %w", err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parsing agent card JSON: %w", err)
	}

	return schema.Validate(doc)
}

// validateTask validates a Task against task.schema.json.
func validateTask(task *a2a.Task) error {
	schema, err := loadSchema("task.schema.json")
	if err != nil {
		return fmt.Errorf("loading task schema: %w", err)
	}

	data, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshaling task: %w", err)
	}

	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parsing task JSON: %w", err)
	}

	return schema.Validate(doc)
}

// validateJSON validates raw JSON bytes against a compiled schema.
func validateJSON(schema *jsonschema.Schema, data []byte) error {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("parsing JSON: %w", err)
	}
	return schema.Validate(doc)
}

// mustMarshal marshals a Go value to JSON, panicking on error.
func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mustMarshal: %v", err))
	}
	return data
}

// referenceAgentCard returns a minimal valid AgentCard for testing.
func referenceAgentCard() a2a.AgentCard {
	return a2a.AgentCard{
		Name:               "reference-agent",
		Description:        "A minimal reference A2A agent for conformance testing",
		Version:            "1.0.0",
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text"},
		Skills: []a2a.AgentSkill{
			{
				ID:          "echo",
				Name:        "Echo",
				Description: "Echoes the input back as output",
				Tags:        []string{"test", "echo"},
			},
		},
	}
}

// newReferenceHandler returns an http.Handler that implements a Level 1 conformant A2A server.
// It echoes the input text back as a completed task artifact.
func newReferenceHandler() http.Handler {
	mux := http.NewServeMux()

	card := referenceAgentCard()
	cardJSON, _ := json.Marshal(card)

	mux.HandleFunc("GET /.well-known/agent-card.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cardJSON)
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			http.Error(w, `{"error":"unsupported content type"}`, http.StatusUnsupportedMediaType)
			return
		}

		var req a2a.SendMessageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			task := a2a.Task{
				ID:     "error",
				Status: a2a.TaskStatus{State: a2a.TaskStateFailed, Message: &a2a.Message{MessageID: "err-msg", Role: "agent", Parts: []a2a.Part{{Text: "invalid request: " + err.Error()}}}},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(task)
			return
		}

		// Determine task ID: prefer taskId, fall back to messageId
		taskID := req.Message.TaskID
		if taskID == "" {
			taskID = req.Message.MessageID
		}

		promptText := req.Message.ExtractPromptText()
		if promptText == "" {
			task := a2a.Task{
				ID: taskID,
				Status: a2a.TaskStatus{
					State:   a2a.TaskStateFailed,
					Message: &a2a.Message{MessageID: "fail-" + taskID, Role: "agent", Parts: []a2a.Part{{Text: "no text content in message parts"}}},
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
					Parts:      []a2a.Part{{Text: "Echo: " + promptText}},
				},
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(task)
	})

	return mux
}
