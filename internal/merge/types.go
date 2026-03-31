// Package merge combines worker branches into a single target branch and
// optionally creates a pull request. It is the final phase of a multi-agent
// orchestration pipeline: the Scheduler runs tasks in dependency order;
// each worker pushes its changes to an isolated branch; the Merger brings
// those branches together.
package merge

import (
	"errors"
	"fmt"
)

// MergeRequest describes which worker branches to merge and where.
type MergeRequest struct {
	ContextID      string         // Shared context linking all tasks
	BaseBranch     string         // Branch to fork from (e.g., "main")
	TargetBranch   string         // New branch that receives all merges
	WorkerBranches []WorkerBranch // Worker branches in merge order
	RepoDir        string         // Local git repository path
	PROptions      *PROptions     // If non-nil, open a PR after merge
}

// WorkerBranch represents a branch produced by a single worker task.
type WorkerBranch struct {
	TaskID  string // Scheduler task ID
	SkillID string // Skill that executed the task
	Branch  string // Full branch name (e.g., "agent/feature/copilot/abc123")
	Status  string // "completed", "failed", "skipped", etc.
}

// MergeResult describes the outcome of a merge operation.
type MergeResult struct {
	ContextID      string
	TargetBranch   string
	MergedBranches []string       // Branches successfully merged
	Skipped        []SkipInfo     // Branches that were skipped (non-completed or conflict)
	Conflicts      []ConflictInfo // Branches that caused merge conflicts
	FileOverlaps   []OverlapInfo  // Pre-merge file overlap warnings
	CommitSHA      string         // HEAD SHA on target branch after all merges
	PR             *PRResult      // Non-nil if a PR was created
}

// SkipInfo explains why a branch was not merged.
type SkipInfo struct {
	Branch string
	Reason string
}

// ConflictInfo describes a merge conflict for a single branch.
type ConflictInfo struct {
	Branch string   // The branch whose merge conflicted
	Files  []string // Conflicting files (from git diff --name-only --diff-filter=U)
}

// OverlapInfo warns that multiple worker branches modified the same file.
// This does not necessarily mean a conflict will occur, but it is worth
// logging because sequential merge order matters.
type OverlapInfo struct {
	File     string   // Path relative to repo root
	Branches []string // Branches that modified this file
}

// PROptions configures pull request creation.
type PROptions struct {
	Owner  string   // Repository owner (GitHub user or org)
	Repo   string   // Repository name
	Title  string   // PR title
	Body   string   // PR body (Markdown)
	Labels []string // Labels to apply
	Draft  bool     // Create as draft PR
}

// PRResult describes a successfully created pull request.
type PRResult struct {
	Number int
	URL    string
}

// Validate checks that a MergeRequest is well-formed.
func (r MergeRequest) Validate() error {
	if r.ContextID == "" {
		return errors.New("merge: ContextID is required")
	}
	if r.BaseBranch == "" {
		return errors.New("merge: BaseBranch is required")
	}
	if r.TargetBranch == "" {
		return errors.New("merge: TargetBranch is required")
	}
	if r.BaseBranch == r.TargetBranch {
		return errors.New("merge: BaseBranch and TargetBranch must differ")
	}
	if r.RepoDir == "" {
		return errors.New("merge: RepoDir is required")
	}
	if len(r.WorkerBranches) == 0 {
		return errors.New("merge: at least one WorkerBranch is required")
	}
	for i, wb := range r.WorkerBranches {
		if wb.Branch == "" {
			return fmt.Errorf("merge: WorkerBranch[%d]: Branch is required", i)
		}
	}
	if r.PROptions != nil {
		if r.PROptions.Owner == "" {
			return errors.New("merge: PROptions.Owner is required")
		}
		if r.PROptions.Repo == "" {
			return errors.New("merge: PROptions.Repo is required")
		}
	}
	return nil
}
