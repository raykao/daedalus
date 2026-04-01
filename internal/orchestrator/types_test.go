package orchestrator_test

import (
	"errors"
	"testing"

	"github.com/raykao/daedalus/internal/dispatch"
	"github.com/raykao/daedalus/internal/orchestrator"
)

// ---------------------------------------------------------------------------
// FanOutRequest.Validate
// ---------------------------------------------------------------------------

func TestFanOutRequest_ValidateNoTasks(t *testing.T) {
	req := orchestrator.FanOutRequest{Tasks: nil}
	if err := req.Validate(); err == nil {
		t.Error("expected error for empty task list, got nil")
	}
}

func TestFanOutRequest_ValidateMissingSkillID(t *testing.T) {
	req := orchestrator.FanOutRequest{
		Tasks: []dispatch.TaskSpec{
			{SkillID: "", Prompt: "do something"},
		},
	}
	if err := req.Validate(); err == nil {
		t.Error("expected error for missing SkillID, got nil")
	}
}

func TestFanOutRequest_ValidateMissingPrompt(t *testing.T) {
	req := orchestrator.FanOutRequest{
		Tasks: []dispatch.TaskSpec{
			{SkillID: "summarize", Prompt: ""},
		},
	}
	if err := req.Validate(); err == nil {
		t.Error("expected error for missing Prompt, got nil")
	}
}

func TestFanOutRequest_ValidateSuccess(t *testing.T) {
	req := orchestrator.FanOutRequest{
		Tasks: []dispatch.TaskSpec{
			{SkillID: "summarize", Prompt: "Summarize this."},
			{SkillID: "translate", Prompt: "Translate that."},
		},
	}
	if err := req.Validate(); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestFanOutRequest_ValidateMultipleTasksOneInvalid(t *testing.T) {
	req := orchestrator.FanOutRequest{
		Tasks: []dispatch.TaskSpec{
			{SkillID: "summarize", Prompt: "Summarize."},
			{SkillID: "", Prompt: "Missing skill."},
		},
	}
	if err := req.Validate(); err == nil {
		t.Error("expected error for task with missing SkillID, got nil")
	}
}

// ---------------------------------------------------------------------------
// TaskResult zero-value behaviour
// ---------------------------------------------------------------------------

func TestTaskResult_ErrorField(t *testing.T) {
	tr := orchestrator.TaskResult{
		Error: errors.New("something failed"),
	}
	if tr.Error == nil {
		t.Error("expected non-nil Error")
	}
}

func TestTaskSpec_TagsOptional(t *testing.T) {
	spec := dispatch.TaskSpec{
		SkillID: "summarize",
		Prompt:  "Summarize this.",
	}
	// Tags field should default to nil (no allocation needed).
	if spec.Tags != nil {
		t.Errorf("expected nil Tags, got %v", spec.Tags)
	}
}

func TestTaskSpec_MetadataOptional(t *testing.T) {
	spec := dispatch.TaskSpec{
		SkillID: "summarize",
		Prompt:  "Summarize this.",
	}
	if spec.Metadata != nil {
		t.Errorf("expected nil Metadata, got %v", spec.Metadata)
	}
}
