package branch

import (
	"fmt"
	"strings"
)

// Prefix is the namespace for all agent worker branches.
const Prefix = "agent/"

// BranchName represents a structured agent worker branch name.
type BranchName struct {
	Feature   string
	AgentName string
	SessionID string
}

// New constructs a BranchName, sanitizing each component.
// Returns an error if any component is empty after sanitization.
func New(feature, agentName, sessionID string) (BranchName, error) {
	b := BranchName{
		Feature:   Sanitize(feature),
		AgentName: Sanitize(agentName),
		SessionID: Sanitize(sessionID),
	}
	if b.Feature == "" {
		return BranchName{}, fmt.Errorf("branch: feature is empty after sanitization")
	}
	if b.AgentName == "" {
		return BranchName{}, fmt.Errorf("branch: agentName is empty after sanitization")
	}
	if b.SessionID == "" {
		return BranchName{}, fmt.Errorf("branch: sessionID is empty after sanitization")
	}
	return b, nil
}

// String returns the full branch ref: agent/<feature>/<agent>/<session-id>
func (b BranchName) String() string {
	return Prefix + b.Feature + "/" + b.AgentName + "/" + b.SessionID
}

// Parse parses a full branch ref into a BranchName.
// Expected format: agent/<feature>/<agent>/<session-id>
func Parse(ref string) (BranchName, error) {
	// Strip "origin/" prefix that git branch -r adds
	ref = strings.TrimPrefix(ref, "origin/")

	if !strings.HasPrefix(ref, Prefix) {
		return BranchName{}, fmt.Errorf("branch: ref %q does not start with %q", ref, Prefix)
	}
	rest := strings.TrimPrefix(ref, Prefix)

	parts := strings.SplitN(rest, "/", 3)
	if len(parts) != 3 {
		return BranchName{}, fmt.Errorf("branch: ref %q has %d segment(s) after prefix, want 3", ref, len(parts))
	}
	for i, p := range parts {
		if p == "" {
			return BranchName{}, fmt.Errorf("branch: segment %d of ref %q is empty", i, ref)
		}
	}
	return BranchName{
		Feature:   parts[0],
		AgentName: parts[1],
		SessionID: parts[2],
	}, nil
}

// Sanitize replaces characters that are invalid in git branch names with hyphens.
// It also collapses consecutive hyphens and trims leading/trailing hyphens.
func Sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isValidBranchChar(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	result := b.String()
	// Collapse consecutive hyphens
	for strings.Contains(result, "--") {
		result = strings.ReplaceAll(result, "--", "-")
	}
	result = strings.Trim(result, "-")
	return result
}

// isValidBranchChar reports whether r is allowed in a git branch name component.
// Disallowed: space, ~, ^, :, ?, *, [, \, ., @, control chars, DEL, and /
// Slashes are also disallowed here because they are used as delimiters.
func isValidBranchChar(r rune) bool {
	if r <= 0x1f || r == 0x7f {
		return false
	}
	switch r {
	case ' ', '~', '^', ':', '?', '*', '[', '\\', '.', '@', '/':
		return false
	}
	return true
}
