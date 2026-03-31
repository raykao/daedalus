package contract

import (
	"testing"

	"github.com/raykao/agent-forge/internal/a2a"
)

func TestA2AMessageFormat(t *testing.T) {
	schema, err := loadSchema("message.schema.json")
	if err != nil {
		t.Fatalf("loading message schema: %v", err)
	}

	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name: "valid minimal message",
			input: mustMarshal(a2a.Message{
				MessageID: "msg-1",
				Role:      "user",
				Parts:     []a2a.Part{{Text: "hello"}},
			}),
			wantErr: false,
		},
		{
			name: "valid full message with all fields",
			input: mustMarshal(a2a.Message{
				MessageID: "msg-2",
				ContextID: "ctx-1",
				TaskID:    "task-1",
				Role:      "agent",
				Parts: []a2a.Part{
					{Text: "response text", MediaType: "text/plain"},
				},
				Metadata: map[string]any{"source": "test", "version": 1},
			}),
			wantErr: false,
		},
		{
			name: "valid message with metadata",
			input: mustMarshal(a2a.Message{
				MessageID: "msg-3",
				Role:      "user",
				Parts:     []a2a.Part{{Text: "query"}},
				Metadata:  map[string]any{"priority": "high"},
			}),
			wantErr: false,
		},
		{
			name:    "invalid missing messageId",
			input:   []byte(`{"role":"user","parts":[{"text":"hello"}]}`),
			wantErr: true,
		},
		{
			name:    "invalid missing role",
			input:   []byte(`{"messageId":"msg-1","parts":[{"text":"hello"}]}`),
			wantErr: true,
		},
		{
			name:    "invalid missing parts",
			input:   []byte(`{"messageId":"msg-1","role":"user"}`),
			wantErr: true,
		},
		{
			name:    "invalid role value",
			input:   []byte(`{"messageId":"msg-1","role":"system","parts":[{"text":"hello"}]}`),
			wantErr: true,
		},
		{
			name:    "invalid empty parts array",
			input:   []byte(`{"messageId":"msg-1","role":"user","parts":[]}`),
			wantErr: true,
		},
		{
			name:    "valid message with data part",
			input:   []byte(`{"messageId":"msg-d","role":"user","parts":[{"data":{"key":"value"}}]}`),
			wantErr: false,
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

func TestA2ASendMessageRequestFormat(t *testing.T) {
	schema, err := loadSchema("send-message-request.schema.json")
	if err != nil {
		t.Fatalf("loading send-message-request schema: %v", err)
	}

	histLen := 5

	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name: "valid minimal send message request",
			input: mustMarshal(a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-1",
					Role:      "user",
					Parts:     []a2a.Part{{Text: "do something"}},
				},
			}),
			wantErr: false,
		},
		{
			name: "valid request with configuration",
			input: mustMarshal(a2a.SendMessageRequest{
				Message: a2a.Message{
					MessageID: "msg-2",
					Role:      "user",
					Parts:     []a2a.Part{{Text: "do something"}},
				},
				Configuration: &a2a.SendMessageConfiguration{
					AcceptedOutputModes: []string{"text", "image"},
					HistoryLength:       &histLen,
				},
			}),
			wantErr: false,
		},
		{
			name:    "invalid missing message field",
			input:   []byte(`{"configuration":{"acceptedOutputModes":["text"]}}`),
			wantErr: true,
		},
		{
			name:    "invalid message with missing messageId",
			input:   []byte(`{"message":{"role":"user","parts":[{"text":"hi"}]}}`),
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

func TestA2ATaskFormat(t *testing.T) {
	schema, err := loadSchema("task.schema.json")
	if err != nil {
		t.Fatalf("loading task schema: %v", err)
	}

	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name: "valid task submitted state",
			input: mustMarshal(a2a.Task{
				ID:     "task-1",
				Status: a2a.TaskStatus{State: a2a.TaskStateSubmitted},
			}),
			wantErr: false,
		},
		{
			name: "valid task working state",
			input: mustMarshal(a2a.Task{
				ID:     "task-2",
				Status: a2a.TaskStatus{State: a2a.TaskStateWorking},
			}),
			wantErr: false,
		},
		{
			name: "valid task completed state",
			input: mustMarshal(a2a.Task{
				ID:     "task-3",
				Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
			}),
			wantErr: false,
		},
		{
			name: "valid task failed state",
			input: mustMarshal(a2a.Task{
				ID:     "task-4",
				Status: a2a.TaskStatus{State: a2a.TaskStateFailed},
			}),
			wantErr: false,
		},
		{
			name: "valid task canceled state",
			input: mustMarshal(a2a.Task{
				ID:     "task-5",
				Status: a2a.TaskStatus{State: a2a.TaskStateCanceled},
			}),
			wantErr: false,
		},
		{
			name: "valid task input-required state",
			input: mustMarshal(a2a.Task{
				ID:     "task-6",
				Status: a2a.TaskStatus{State: a2a.TaskStateInputRequired},
			}),
			wantErr: false,
		},
		{
			name: "valid task rejected state",
			input: mustMarshal(a2a.Task{
				ID:     "task-7",
				Status: a2a.TaskStatus{State: a2a.TaskStateRejected},
			}),
			wantErr: false,
		},
		{
			name: "valid task with artifacts",
			input: mustMarshal(a2a.Task{
				ID:     "task-8",
				Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
				Artifacts: []a2a.Artifact{
					{
						ArtifactID:  "artifact-1",
						Name:        "output.txt",
						Description: "generated output",
						Parts:       []a2a.Part{{Text: "result"}},
					},
				},
			}),
			wantErr: false,
		},
		{
			name: "valid task with history",
			input: mustMarshal(a2a.Task{
				ID:     "task-9",
				Status: a2a.TaskStatus{State: a2a.TaskStateCompleted},
				History: []a2a.Message{
					{MessageID: "msg-1", Role: "user", Parts: []a2a.Part{{Text: "request"}}},
					{MessageID: "msg-2", Role: "agent", Parts: []a2a.Part{{Text: "response"}}},
				},
			}),
			wantErr: false,
		},
		{
			name:    "invalid missing id",
			input:   []byte(`{"status":{"state":"submitted"}}`),
			wantErr: true,
		},
		{
			name:    "invalid missing status",
			input:   []byte(`{"id":"task-1"}`),
			wantErr: true,
		},
		{
			name:    "invalid state value",
			input:   []byte(`{"id":"task-1","status":{"state":"unknown"}}`),
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
