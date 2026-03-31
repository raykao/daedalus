package merge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// These tests create real git repos in temp directories to validate
// the ExecGit implementation against actual git behavior.

func TestExecGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	// Set up a bare remote and a working clone.
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "remote.git")
	work := filepath.Join(tmp, "work")

	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return string(out)
	}

	// Create bare repo
	os.MkdirAll(bare, 0o755)
	run(bare, "init", "--bare")

	// Clone it
	run(tmp, "clone", bare, "work")
	run(work, "config", "user.email", "test@test.com")
	run(work, "config", "user.name", "Test")

	// Create initial commit on main
	os.WriteFile(filepath.Join(work, "README.md"), []byte("# Hello\n"), 0o644)
	run(work, "add", ".")
	run(work, "commit", "-m", "initial")
	run(work, "push", "origin", "HEAD")

	ctx := context.Background()
	g := &ExecGit{RepoDir: work}

	t.Run("Fetch", func(t *testing.T) {
		if err := g.Fetch(ctx); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
	})

	t.Run("HeadSHA", func(t *testing.T) {
		sha, err := g.HeadSHA(ctx)
		if err != nil {
			t.Fatalf("HeadSHA: %v", err)
		}
		if len(sha) != 40 {
			t.Fatalf("expected 40-char SHA, got %d: %s", len(sha), sha)
		}
	})

	t.Run("BranchExists", func(t *testing.T) {
		// main exists locally
		if !g.BranchExists(ctx, "main") {
			t.Fatal("expected main to exist")
		}
		// origin/main exists as remote tracking
		if !g.BranchExists(ctx, "origin/main") {
			t.Fatal("expected origin/main to exist")
		}
		// nonexistent
		if g.BranchExists(ctx, "no-such-branch") {
			t.Fatal("expected no-such-branch to not exist")
		}
	})

	t.Run("CreateBranch", func(t *testing.T) {
		if err := g.CreateBranch(ctx, "test-branch", "main"); err != nil {
			t.Fatalf("CreateBranch: %v", err)
		}
		if !g.BranchExists(ctx, "test-branch") {
			t.Fatal("expected test-branch to exist after creation")
		}
		// Go back to main for subsequent tests
		run(work, "checkout", "main")
	})

	t.Run("ChangedFiles", func(t *testing.T) {
		// Create a branch with a file change
		run(work, "checkout", "-b", "feature-branch")
		os.WriteFile(filepath.Join(work, "feature.go"), []byte("package main\n"), 0o644)
		run(work, "add", ".")
		run(work, "commit", "-m", "add feature")
		run(work, "push", "origin", "feature-branch")
		run(work, "checkout", "main")

		files, err := g.ChangedFiles(ctx, "feature-branch", "main")
		if err != nil {
			t.Fatalf("ChangedFiles: %v", err)
		}
		if len(files) != 1 || files[0] != "feature.go" {
			t.Fatalf("expected [feature.go], got %v", files)
		}
	})

	t.Run("MergeBranch_success", func(t *testing.T) {
		// Merge feature-branch into main
		if err := g.MergeBranch(ctx, "feature-branch", "Merge feature"); err != nil {
			t.Fatalf("MergeBranch: %v", err)
		}
		// Verify the file exists
		if _, err := os.Stat(filepath.Join(work, "feature.go")); err != nil {
			t.Fatalf("expected feature.go to exist after merge: %v", err)
		}
	})

	t.Run("MergeBranch_conflict", func(t *testing.T) {
		// Create two branches that modify the same file
		run(work, "checkout", "-b", "conflict-a")
		os.WriteFile(filepath.Join(work, "shared.go"), []byte("// version A\n"), 0o644)
		run(work, "add", ".")
		run(work, "commit", "-m", "version A")
		run(work, "push", "origin", "conflict-a")
		run(work, "checkout", "main")

		run(work, "checkout", "-b", "conflict-b")
		os.WriteFile(filepath.Join(work, "shared.go"), []byte("// version B\n"), 0o644)
		run(work, "add", ".")
		run(work, "commit", "-m", "version B")
		run(work, "push", "origin", "conflict-b")

		// Merge conflict-a first
		run(work, "checkout", "main")
		if err := g.MergeBranch(ctx, "conflict-a", "Merge A"); err != nil {
			t.Fatalf("Merge conflict-a should succeed: %v", err)
		}

		// Merge conflict-b should fail
		err := g.MergeBranch(ctx, "conflict-b", "Merge B")
		if err == nil {
			t.Fatal("expected merge conflict error")
		}

		// ConflictFiles should return shared.go
		files, cfErr := g.ConflictFiles(ctx)
		if cfErr != nil {
			t.Fatalf("ConflictFiles: %v", cfErr)
		}
		if len(files) != 1 || files[0] != "shared.go" {
			t.Fatalf("expected [shared.go], got %v", files)
		}

		// AbortMerge
		if abortErr := g.AbortMerge(ctx); abortErr != nil {
			t.Fatalf("AbortMerge: %v", abortErr)
		}
	})

	t.Run("Push", func(t *testing.T) {
		run(work, "checkout", "-b", "push-test")
		os.WriteFile(filepath.Join(work, "push.txt"), []byte("push test\n"), 0o644)
		run(work, "add", ".")
		run(work, "commit", "-m", "push test commit")

		if err := g.Push(ctx, "push-test"); err != nil {
			t.Fatalf("Push: %v", err)
		}
	})

	t.Run("custom remote", func(t *testing.T) {
		// Add a second remote and verify ExecGit uses it
		run(work, "remote", "add", "upstream", bare)
		gCustom := &ExecGit{RepoDir: work, Remote: "upstream"}

		if err := gCustom.Fetch(ctx); err != nil {
			t.Fatalf("Fetch with custom remote: %v", err)
		}

		// BranchExists should check upstream/main
		if !gCustom.BranchExists(ctx, "upstream/main") {
			t.Fatal("expected upstream/main to exist")
		}
	})

	t.Run("ChangedFiles returns nil for no changes", func(t *testing.T) {
		run(work, "checkout", "main")
		// Push current main so origin/main matches local main
		run(work, "push", "origin", "main")
		// Create a branch at the same commit
		run(work, "checkout", "-b", "no-change-branch")
		run(work, "checkout", "main")

		files, err := g.ChangedFiles(ctx, "no-change-branch", "main")
		if err != nil {
			t.Fatalf("ChangedFiles: %v", err)
		}
		if files != nil {
			t.Fatalf("expected nil for no changes, got %v", files)
		}
	})
}
