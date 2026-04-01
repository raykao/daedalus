package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/raykao/daedalus/internal/a2a"
)

func TestEchoHealthEndpoint(t *testing.T) {
	handler := newEchoHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status: want 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("content-type: want application/json, got %q", ct)
	}

	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding health response: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("status: want \"ok\", got %q", result["status"])
	}
}

func TestEchoAgentCardEndpoint(t *testing.T) {
	handler := newEchoHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/.well-known/agent-card.json")
	if err != nil {
		t.Fatalf("agent card request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent card status: want 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("content-type: want application/json, got %q", ct)
	}

	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decoding agent card: %v", err)
	}

	if card.Name != "echo-agent" {
		t.Errorf("name: want \"echo-agent\", got %q", card.Name)
	}
	if card.Version != "1.0.0" {
		t.Errorf("version: want \"1.0.0\", got %q", card.Version)
	}
	if len(card.Skills) != 1 {
		t.Fatalf("skills count: want 1, got %d", len(card.Skills))
	}
	if card.Skills[0].ID != "echo" {
		t.Errorf("skill ID: want \"echo\", got %q", card.Skills[0].ID)
	}
	if card.Capabilities.Streaming {
		t.Error("streaming should be false")
	}
	if card.Capabilities.PushNotifications {
		t.Error("pushNotifications should be false")
	}
}

func TestEchoTaskEndpoint(t *testing.T) {
	handler := newEchoHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "msg-1",
			TaskID:    "task-1",
			Role:      "user",
			Parts:     []a2a.Part{{Text: "hello world"}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	resp, err := http.Post(srv.URL+"/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("task request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("task status: want 200, got %d", resp.StatusCode)
	}

	var task a2a.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatalf("decoding task: %v", err)
	}

	if task.ID != "task-1" {
		t.Errorf("task ID: want \"task-1\", got %q", task.ID)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("task state: want completed, got %q", task.Status.State)
	}
	if len(task.Artifacts) != 1 {
		t.Fatalf("artifacts count: want 1, got %d", len(task.Artifacts))
	}
	if len(task.Artifacts[0].Parts) != 1 {
		t.Fatalf("artifact parts count: want 1, got %d", len(task.Artifacts[0].Parts))
	}

	want := "echo: hello world"
	got := task.Artifacts[0].Parts[0].Text
	if got != want {
		t.Errorf("echo text: want %q, got %q", want, got)
	}
}

func TestEchoTaskFallbackToMessageID(t *testing.T) {
	handler := newEchoHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "msg-fallback",
			Role:      "user",
			Parts:     []a2a.Part{{Text: "test"}},
		},
	}
	body, _ := json.Marshal(req)

	resp, err := http.Post(srv.URL+"/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var task a2a.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatalf("decoding task: %v", err)
	}

	// When TaskID is empty, should fall back to MessageID.
	if task.ID != "msg-fallback" {
		t.Errorf("task ID: want \"msg-fallback\", got %q", task.ID)
	}
}

func TestEchoEmptyMessage(t *testing.T) {
	handler := newEchoHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req := a2a.SendMessageRequest{
		Message: a2a.Message{
			MessageID: "msg-empty",
			TaskID:    "task-empty",
			Role:      "user",
			Parts:     []a2a.Part{}, // no text parts
		},
	}
	body, _ := json.Marshal(req)

	resp, err := http.Post(srv.URL+"/", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: want 200, got %d", resp.StatusCode)
	}

	var task a2a.Task
	if err := json.NewDecoder(resp.Body).Decode(&task); err != nil {
		t.Fatalf("decoding task: %v", err)
	}

	if task.Status.State != a2a.TaskStateFailed {
		t.Errorf("task state: want failed, got %q", task.Status.State)
	}
	if task.Status.Message == nil {
		t.Fatal("expected a status message for failed task")
	}
	if task.Status.Message.Parts[0].Text != "no text content in message parts" {
		t.Errorf("unexpected error message: %q", task.Status.Message.Parts[0].Text)
	}
}

func TestEchoInvalidJSON(t *testing.T) {
	handler := newEchoHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/", "application/json", bytes.NewReader([]byte("{invalid")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status: want 400, got %d", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	var errResp map[string]string
	if err := json.Unmarshal(respBody, &errResp); err != nil {
		t.Fatalf("decoding error response: %v", err)
	}
	if _, ok := errResp["error"]; !ok {
		t.Error("expected error field in response")
	}
}

func TestEchoUnsupportedContentType(t *testing.T) {
	handler := newEchoHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/", "text/plain", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("status: want 415, got %d", resp.StatusCode)
	}
}
