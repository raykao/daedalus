package merge

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
)

// mockGitOps implements GitOps for testing.
type mockGitOps struct {
	fetchErr         error
	createBranchErr  error
	branchExistsMap  map[string]bool
	mergeBranchErr   map[string]error // branch -> error
	abortMergeErr    error
	conflictFiles    []string
	conflictFilesErr error
	changedFilesMap  map[string][]string // branch -> files
	changedFilesErr  map[string]error    // branch -> error
	headSHA          string
	headSHAErr       error
	pushErr          error

	// call tracking
	fetchCalls        int
	createBranchCalls []createBranchCall
	mergeCalls        []string // branches merged
	abortMergeCalls   int
	pushCalls         []string
}

type createBranchCall struct {
	name, startPoint string
}

func newMockGitOps() *mockGitOps {
	return &mockGitOps{
		branchExistsMap: make(map[string]bool),
		mergeBranchErr:  make(map[string]error),
		changedFilesMap: make(map[string][]string),
		changedFilesErr: make(map[string]error),
		headSHA:         "abc123def456",
	}
}

func (m *mockGitOps) Fetch(ctx context.Context) error {
	m.fetchCalls++
	return m.fetchErr
}

func (m *mockGitOps) CreateBranch(ctx context.Context, name, startPoint string) error {
	m.createBranchCalls = append(m.createBranchCalls, createBranchCall{name, startPoint})
	return m.createBranchErr
}

func (m *mockGitOps) BranchExists(ctx context.Context, branch string) bool {
	return m.branchExistsMap[branch]
}

func (m *mockGitOps) MergeBranch(ctx context.Context, branch, message string) error {
	m.mergeCalls = append(m.mergeCalls, branch)
	if err, ok := m.mergeBranchErr[branch]; ok {
		return err
	}
	return nil
}

func (m *mockGitOps) AbortMerge(ctx context.Context) error {
	m.abortMergeCalls++
	return m.abortMergeErr
}

func (m *mockGitOps) ConflictFiles(ctx context.Context) ([]string, error) {
	return m.conflictFiles, m.conflictFilesErr
}

func (m *mockGitOps) ChangedFiles(ctx context.Context, branch, base string) ([]string, error) {
	if err, ok := m.changedFilesErr[branch]; ok {
		return nil, err
	}
	files, ok := m.changedFilesMap[branch]
	if !ok {
		return nil, nil
	}
	return files, nil
}

func (m *mockGitOps) HeadSHA(ctx context.Context) (string, error) {
	return m.headSHA, m.headSHAErr
}

func (m *mockGitOps) Push(ctx context.Context, branch string) error {
	m.pushCalls = append(m.pushCalls, branch)
	return m.pushErr
}

// mockPRCreator implements PRCreator for testing.
type mockPRCreator struct {
	result *PRResult
	err    error
	calls  []prCreateCall
}

type prCreateCall struct {
	opts       PROptions
	head, base string
}

func (m *mockPRCreator) CreatePR(ctx context.Context, opts PROptions, head, base string) (*PRResult, error) {
	m.calls = append(m.calls, prCreateCall{opts, head, base})
	return m.result, m.err
}

func TestDetectOverlaps(t *testing.T) {
	ctx := context.Background()
	logger := slog.Default()

	t.Run("no overlaps", func(t *testing.T) {
		git := newMockGitOps()
		git.changedFilesMap["branch-a"] = []string{"file1.go", "file2.go"}
		git.changedFilesMap["branch-b"] = []string{"file3.go", "file4.go"}

		branches := []WorkerBranch{
			{Branch: "branch-a", Status: "completed"},
			{Branch: "branch-b", Status: "completed"},
		}

		overlaps := DetectOverlaps(ctx, git, "main", branches, logger)
		if len(overlaps) != 0 {
			t.Fatalf("expected 0 overlaps, got %d", len(overlaps))
		}
	})

	t.Run("one file overlap", func(t *testing.T) {
		git := newMockGitOps()
		git.changedFilesMap["branch-a"] = []string{"shared.go", "unique-a.go"}
		git.changedFilesMap["branch-b"] = []string{"shared.go", "unique-b.go"}

		branches := []WorkerBranch{
			{Branch: "branch-a", Status: "completed"},
			{Branch: "branch-b", Status: "completed"},
		}

		overlaps := DetectOverlaps(ctx, git, "main", branches, logger)
		if len(overlaps) != 1 {
			t.Fatalf("expected 1 overlap, got %d", len(overlaps))
		}
		if overlaps[0].File != "shared.go" {
			t.Fatalf("expected overlap on shared.go, got %s", overlaps[0].File)
		}
		if len(overlaps[0].Branches) != 2 {
			t.Fatalf("expected 2 branches in overlap, got %d", len(overlaps[0].Branches))
		}
	})

	t.Run("skips non-completed branches", func(t *testing.T) {
		git := newMockGitOps()
		git.changedFilesMap["branch-a"] = []string{"shared.go"}
		git.changedFilesMap["branch-b"] = []string{"shared.go"}

		branches := []WorkerBranch{
			{Branch: "branch-a", Status: "completed"},
			{Branch: "branch-b", Status: "failed"},
		}

		overlaps := DetectOverlaps(ctx, git, "main", branches, logger)
		if len(overlaps) != 0 {
			t.Fatalf("expected 0 overlaps (failed branch excluded), got %d", len(overlaps))
		}
	})

	t.Run("continues when ChangedFiles errors for one branch", func(t *testing.T) {
		git := newMockGitOps()
		git.changedFilesMap["branch-a"] = []string{"file1.go"}
		git.changedFilesErr["branch-b"] = fmt.Errorf("git error")
		git.changedFilesMap["branch-c"] = []string{"file1.go"}

		branches := []WorkerBranch{
			{Branch: "branch-a", Status: "completed"},
			{Branch: "branch-b", Status: "completed"},
			{Branch: "branch-c", Status: "completed"},
		}

		overlaps := DetectOverlaps(ctx, git, "main", branches, logger)
		// branch-b errored, but branch-a and branch-c both touch file1.go
		if len(overlaps) != 1 {
			t.Fatalf("expected 1 overlap, got %d", len(overlaps))
		}
		if overlaps[0].File != "file1.go" {
			t.Fatalf("expected file1.go overlap, got %s", overlaps[0].File)
		}
		if len(overlaps[0].Branches) != 2 {
			t.Fatalf("expected 2 branches (a and c), got %d", len(overlaps[0].Branches))
		}
	})
}
