package merge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestMerger_Merge(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	t.Run("successful merge of two branches", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true
		git.branchExistsMap["branch-test"] = true

		m := NewMerger(git, nil, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-1",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", SkillID: "implement", Branch: "branch-impl", Status: "completed"},
				{TaskID: "t2", SkillID: "test", Branch: "branch-test", Status: "completed"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.MergedBranches) != 2 {
			t.Fatalf("expected 2 merged branches, got %d", len(result.MergedBranches))
		}
		if result.CommitSHA != "abc123def456" {
			t.Fatalf("expected SHA abc123def456, got %s", result.CommitSHA)
		}
		if result.ContextID != "ctx-1" {
			t.Fatalf("expected context ID ctx-1, got %s", result.ContextID)
		}
		if result.TargetBranch != "agent/feature/merged" {
			t.Fatalf("expected target branch agent/feature/merged, got %s", result.TargetBranch)
		}
		if len(git.mergeCalls) != 2 {
			t.Fatalf("expected 2 merge calls, got %d", len(git.mergeCalls))
		}
		if len(git.pushCalls) != 1 || git.pushCalls[0] != "agent/feature/merged" {
			t.Fatalf("expected push to agent/feature/merged, got %v", git.pushCalls)
		}
	})

	t.Run("skips non-completed branches", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true

		m := NewMerger(git, nil, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-2",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
				{TaskID: "t2", Branch: "branch-fail", Status: "failed"},
				{TaskID: "t3", Branch: "branch-skip", Status: "skipped"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.MergedBranches) != 1 {
			t.Fatalf("expected 1 merged branch, got %d", len(result.MergedBranches))
		}
		if len(result.Skipped) != 2 {
			t.Fatalf("expected 2 skipped, got %d", len(result.Skipped))
		}
	})

	t.Run("skips branch not found", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		// "missing-branch" not in branchExistsMap

		m := NewMerger(git, nil, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-3",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "missing-branch", Status: "completed"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.MergedBranches) != 0 {
			t.Fatalf("expected 0 merged branches, got %d", len(result.MergedBranches))
		}
		if len(result.Skipped) != 1 {
			t.Fatalf("expected 1 skipped, got %d", len(result.Skipped))
		}
	})

	t.Run("handles merge conflict", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-a"] = true
		git.branchExistsMap["branch-b"] = true
		git.mergeBranchErr["branch-b"] = fmt.Errorf("merge conflict")
		git.conflictFiles = []string{"shared.go"}

		m := NewMerger(git, nil, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-4",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-a", Status: "completed"},
				{TaskID: "t2", Branch: "branch-b", Status: "completed"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.MergedBranches) != 1 {
			t.Fatalf("expected 1 merged branch, got %d", len(result.MergedBranches))
		}
		if result.MergedBranches[0] != "branch-a" {
			t.Fatalf("expected branch-a merged, got %s", result.MergedBranches[0])
		}
		if len(result.Conflicts) != 1 {
			t.Fatalf("expected 1 conflict, got %d", len(result.Conflicts))
		}
		if result.Conflicts[0].Branch != "branch-b" {
			t.Fatalf("expected conflict on branch-b, got %s", result.Conflicts[0].Branch)
		}
		if len(result.Conflicts[0].Files) != 1 || result.Conflicts[0].Files[0] != "shared.go" {
			t.Fatalf("expected conflict file shared.go, got %v", result.Conflicts[0].Files)
		}
		if git.abortMergeCalls != 1 {
			t.Fatalf("expected 1 abort call, got %d", git.abortMergeCalls)
		}
	})

	t.Run("no branches merged returns empty result", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true

		m := NewMerger(git, nil, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-5",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-fail", Status: "failed"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.MergedBranches) != 0 {
			t.Fatalf("expected 0 merged, got %d", len(result.MergedBranches))
		}
		// Should not push or get SHA when nothing merged
		if len(git.pushCalls) != 0 {
			t.Fatalf("expected 0 push calls, got %d", len(git.pushCalls))
		}
	})

	t.Run("creates PR when PROptions set", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true

		prCreator := &mockPRCreator{
			result: &PRResult{Number: 42, URL: "https://github.com/org/repo/pull/42"},
		}

		m := NewMerger(git, prCreator, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-6",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", SkillID: "implement", Branch: "branch-impl", Status: "completed"},
			},
			PROptions: &PROptions{
				Owner: "raykao",
				Repo:  "agent-forge",
				Title: "Test PR",
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if result.PR == nil {
			t.Fatal("expected PR result")
		}
		if result.PR.Number != 42 {
			t.Fatalf("expected PR #42, got #%d", result.PR.Number)
		}
		if len(prCreator.calls) != 1 {
			t.Fatalf("expected 1 PR create call, got %d", len(prCreator.calls))
		}
		if prCreator.calls[0].head != "agent/feature/merged" {
			t.Fatalf("expected head agent/feature/merged, got %s", prCreator.calls[0].head)
		}
		if prCreator.calls[0].base != "main" {
			t.Fatalf("expected base main, got %s", prCreator.calls[0].base)
		}
	})

	t.Run("PR failure is non-fatal", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true

		prCreator := &mockPRCreator{
			err: fmt.Errorf("gh: auth required"),
		}

		m := NewMerger(git, prCreator, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-7",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
			},
			PROptions: &PROptions{Owner: "raykao", Repo: "agent-forge"},
		})

		// Error is returned but branch was pushed
		if err == nil {
			t.Fatal("expected error for PR failure")
		}
		if result == nil {
			t.Fatal("expected non-nil result despite PR failure")
		}
		if len(result.MergedBranches) != 1 {
			t.Fatalf("expected 1 merged branch, got %d", len(result.MergedBranches))
		}
		if result.PR != nil {
			t.Fatal("expected nil PR on failure")
		}
	})

	t.Run("abort merge failure is fatal", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-a"] = true
		git.mergeBranchErr["branch-a"] = fmt.Errorf("merge conflict")
		git.abortMergeErr = fmt.Errorf("abort failed: not in merge state")

		m := NewMerger(git, nil, logger)
		_, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-abort",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-a", Status: "completed"},
			},
		})
		if err == nil {
			t.Fatal("expected fatal error when abort merge fails")
		}
		if !strings.Contains(err.Error(), "failed to abort") {
			t.Fatalf("expected abort error message, got: %v", err)
		}
	})

	t.Run("validation failure", func(t *testing.T) {
		m := NewMerger(newMockGitOps(), nil, logger)
		_, err := m.Merge(ctx, MergeRequest{})
		if err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("fetch failure", func(t *testing.T) {
		git := newMockGitOps()
		git.fetchErr = fmt.Errorf("network error")

		m := NewMerger(git, nil, logger)
		_, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-8",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
			},
		})
		if err == nil {
			t.Fatal("expected fetch error")
		}
	})

	t.Run("uses base branch directly when origin not found", func(t *testing.T) {
		git := newMockGitOps()
		// origin/main not in branchExistsMap
		git.branchExistsMap["branch-impl"] = true

		m := NewMerger(git, nil, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-9",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.MergedBranches) != 1 {
			t.Fatalf("expected 1 merged, got %d", len(result.MergedBranches))
		}
		// Should have used "main" as startPoint (not "origin/main")
		if len(git.createBranchCalls) != 1 {
			t.Fatalf("expected 1 createBranch call, got %d", len(git.createBranchCalls))
		}
		if git.createBranchCalls[0].startPoint != "main" {
			t.Fatalf("expected startPoint 'main', got %q", git.createBranchCalls[0].startPoint)
		}
	})

	t.Run("generates default PR title and body", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true
		git.branchExistsMap["branch-test"] = true

		prCreator := &mockPRCreator{
			result: &PRResult{Number: 99, URL: "https://github.com/org/repo/pull/99"},
		}

		m := NewMerger(git, prCreator, logger)
		_, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-10",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", SkillID: "implement", Branch: "branch-impl", Status: "completed"},
				{TaskID: "t2", SkillID: "test", Branch: "branch-test", Status: "completed"},
			},
			PROptions: &PROptions{Owner: "raykao", Repo: "agent-forge"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(prCreator.calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(prCreator.calls))
		}
		call := prCreator.calls[0]
		if call.opts.Title == "" {
			t.Fatal("expected auto-generated title")
		}
		if call.opts.Body == "" {
			t.Fatal("expected auto-generated body")
		}
		// Verify body contains the context and branch info
		if !strings.Contains(call.opts.Body, "ctx-10") {
			t.Fatal("expected body to contain context ID")
		}
		if !strings.Contains(call.opts.Body, "branch-impl") {
			t.Fatal("expected body to contain merged branch name")
		}
	})

	t.Run("nil logger uses default", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true

		m := NewMerger(git, nil, nil) // nil logger
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-nil-logger",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.MergedBranches) != 1 {
			t.Fatalf("expected 1 merged, got %d", len(result.MergedBranches))
		}
	})

	t.Run("push failure is fatal", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true
		git.pushErr = fmt.Errorf("push rejected")

		m := NewMerger(git, nil, logger)
		_, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-push-fail",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
			},
		})
		if err == nil {
			t.Fatal("expected push error")
		}
		if !strings.Contains(err.Error(), "push") {
			t.Fatalf("expected push error message, got: %v", err)
		}
	})

	t.Run("create branch failure is fatal", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.createBranchErr = fmt.Errorf("branch already exists")

		m := NewMerger(git, nil, logger)
		_, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-branch-fail",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
			},
		})
		if err == nil {
			t.Fatal("expected create branch error")
		}
	})

	t.Run("HeadSHA failure is fatal", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-impl"] = true
		git.headSHAErr = fmt.Errorf("detached HEAD")

		m := NewMerger(git, nil, logger)
		_, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-sha-fail",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-impl", Status: "completed"},
			},
		})
		if err == nil {
			t.Fatal("expected HeadSHA error")
		}
		if !strings.Contains(err.Error(), "HEAD SHA") {
			t.Fatalf("expected HEAD SHA error message, got: %v", err)
		}
	})

	t.Run("ConflictFiles error is non-fatal", func(t *testing.T) {
		git := newMockGitOps()
		git.branchExistsMap["origin/main"] = true
		git.branchExistsMap["branch-a"] = true
		git.branchExistsMap["branch-b"] = true
		git.mergeBranchErr["branch-a"] = fmt.Errorf("conflict")
		git.conflictFilesErr = fmt.Errorf("git diff failed")

		m := NewMerger(git, nil, logger)
		result, err := m.Merge(ctx, MergeRequest{
			ContextID:    "ctx-cf-err",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
			RepoDir:      "/tmp/repo",
			WorkerBranches: []WorkerBranch{
				{TaskID: "t1", Branch: "branch-a", Status: "completed"},
				{TaskID: "t2", Branch: "branch-b", Status: "completed"},
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// branch-a conflicted (with ConflictFiles error), branch-b should still merge
		if len(result.MergedBranches) != 1 || result.MergedBranches[0] != "branch-b" {
			t.Fatalf("expected branch-b merged, got %v", result.MergedBranches)
		}
		if len(result.Conflicts) != 1 {
			t.Fatalf("expected 1 conflict, got %d", len(result.Conflicts))
		}
		// Files should be nil since ConflictFiles errored
		if result.Conflicts[0].Files != nil {
			t.Fatalf("expected nil conflict files, got %v", result.Conflicts[0].Files)
		}
	})
}

func TestBuildPRBody(t *testing.T) {
	t.Run("includes all sections", func(t *testing.T) {
		req := MergeRequest{
			ContextID:    "ctx-body-test",
			BaseBranch:   "main",
			TargetBranch: "agent/feature/merged",
		}
		result := &MergeResult{
			MergedBranches: []string{"branch-a", "branch-b"},
			Skipped: []SkipInfo{
				{Branch: "branch-c", Reason: "task status is \"failed\""},
			},
			Conflicts: []ConflictInfo{
				{Branch: "branch-d", Files: []string{"shared.go", "util.go"}},
			},
			FileOverlaps: []OverlapInfo{
				{File: "shared.go", Branches: []string{"branch-a", "branch-d"}},
			},
		}

		body := buildPRBody(req, result)

		// Context and branches
		if !strings.Contains(body, "ctx-body-test") {
			t.Fatal("expected body to contain context ID")
		}
		if !strings.Contains(body, "`main`") {
			t.Fatal("expected body to contain base branch")
		}

		// Merged section
		if !strings.Contains(body, "### Merged (2)") {
			t.Fatal("expected Merged section with count 2")
		}
		if !strings.Contains(body, "✅ `branch-a`") {
			t.Fatal("expected merged branch-a")
		}

		// Skipped section
		if !strings.Contains(body, "### Skipped (1)") {
			t.Fatal("expected Skipped section")
		}
		if !strings.Contains(body, "⏭️ `branch-c`") {
			t.Fatal("expected skipped branch-c")
		}

		// Conflicts section
		if !strings.Contains(body, "### Conflicts (1)") {
			t.Fatal("expected Conflicts section")
		}
		if !strings.Contains(body, "⚠️ `branch-d`") {
			t.Fatal("expected conflicted branch-d")
		}
		if !strings.Contains(body, "`shared.go`") {
			t.Fatal("expected conflict file shared.go")
		}

		// File overlaps section
		if !strings.Contains(body, "File Overlaps") {
			t.Fatal("expected File Overlaps section")
		}
		if !strings.Contains(body, "`shared.go` modified by:") {
			t.Fatal("expected overlap entry")
		}
	})

	t.Run("conflict without files", func(t *testing.T) {
		req := MergeRequest{
			ContextID:    "ctx-no-files",
			BaseBranch:   "main",
			TargetBranch: "merged",
		}
		result := &MergeResult{
			MergedBranches: []string{"branch-ok"},
			Conflicts: []ConflictInfo{
				{Branch: "branch-bad", Files: nil},
			},
		}

		body := buildPRBody(req, result)

		if !strings.Contains(body, "⚠️ `branch-bad`") {
			t.Fatal("expected conflict entry")
		}
		// Should NOT contain " — files:" when no files listed
		if strings.Contains(body, "— files:") {
			t.Fatal("expected no files clause when Files is nil")
		}
	})

	t.Run("no skipped or conflicts omits those sections", func(t *testing.T) {
		req := MergeRequest{
			ContextID:    "ctx-clean",
			BaseBranch:   "main",
			TargetBranch: "merged",
		}
		result := &MergeResult{
			MergedBranches: []string{"branch-only"},
		}

		body := buildPRBody(req, result)

		if strings.Contains(body, "Skipped") {
			t.Fatal("expected no Skipped section")
		}
		if strings.Contains(body, "Conflicts") {
			t.Fatal("expected no Conflicts section")
		}
		if strings.Contains(body, "File Overlaps") {
			t.Fatal("expected no File Overlaps section")
		}
	})
}

func TestJoinQuoted(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{"single", []string{"a.go"}, "`a.go`"},
		{"two", []string{"a.go", "b.go"}, "`a.go`, `b.go`"},
		{"three", []string{"x", "y", "z"}, "`x`, `y`, `z`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinQuoted(tt.input)
			if got != tt.want {
				t.Fatalf("joinQuoted(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
