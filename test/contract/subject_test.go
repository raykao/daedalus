package contract

import (
	"regexp"
	"testing"
)

// subjectPattern enforces the queue subject naming convention: agent.tasks.<agent-name>
// where agent-name is lowercase alphanumeric starting with a letter, hyphens allowed.
var subjectPattern = regexp.MustCompile(`^agent\.tasks\.[a-z][a-z0-9-]*$`)

func validateSubject(subject string) error {
	if !subjectPattern.MatchString(subject) {
		return &subjectError{subject: subject}
	}
	return nil
}

type subjectError struct {
	subject string
}

func (e *subjectError) Error() string {
	return "invalid queue subject: " + e.subject + " (must match ^agent\\.tasks\\.[a-z][a-z0-9-]*$)"
}

func TestQueueSubjectNaming(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		wantErr bool
	}{
		{
			name:    "valid copilot subject",
			subject: "agent.tasks.copilot",
			wantErr: false,
		},
		{
			name:    "valid hyphenated subject",
			subject: "agent.tasks.claude-code",
			wantErr: false,
		},
		{
			name:    "valid multi-segment name with version",
			subject: "agent.tasks.my-agent-v2",
			wantErr: false,
		},
		{
			name:    "valid single letter name",
			subject: "agent.tasks.a",
			wantErr: false,
		},
		{
			name:    "valid alphanumeric name",
			subject: "agent.tasks.agent123",
			wantErr: false,
		},
		{
			name:    "invalid missing tasks segment",
			subject: "agent.copilot",
			wantErr: true,
		},
		{
			name:    "invalid empty agent name",
			subject: "agent.tasks.",
			wantErr: true,
		},
		{
			name:    "invalid uppercase in name",
			subject: "agent.tasks.MY_AGENT",
			wantErr: true,
		},
		{
			name:    "invalid wrong prefix order",
			subject: "tasks.agent.copilot",
			wantErr: true,
		},
		{
			name:    "invalid spaces in name",
			subject: "agent.tasks.agent name",
			wantErr: true,
		},
		{
			name:    "invalid starts with hyphen",
			subject: "agent.tasks.-invalid",
			wantErr: true,
		},
		{
			name:    "invalid underscore in name",
			subject: "agent.tasks.my_agent",
			wantErr: true,
		},
		{
			name:    "invalid empty string",
			subject: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSubject(tt.subject)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSubject(%q) error = %v, wantErr %v", tt.subject, err, tt.wantErr)
			}
		})
	}
}
