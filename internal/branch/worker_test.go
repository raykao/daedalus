package branch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initGitRepo creates a bare git repo and a working repo with an initial commit.
// Returns (workDir, bareDir).
func initGitRepo(t *testing.T) (string, string) {
	t.Helper()

	bare := filepath.Join(t.TempDir(), "remote.git")
	work := filepath.Join(t.TempDir(), "repo")

	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}

	// Bare remote
	if err := os.MkdirAll(bare, 0755); err != nil {
		t.Fatalf("mkdir bare: %v", err)
	}
	runGit(bare, "init", "--bare")

	// Working repo
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	runGit(work, "init")
	runGit(work, "config", "user.email", "test@test.com")
	runGit(work, "config", "user.name", "Test")
	runGit(work, "remote", "add", "origin", bare)

	// Create an initial commit so checkout works
	readme := filepath.Join(work, "README.md")
	if err := os.WriteFile(readme, []byte("# test\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(work, "add", ".")
	runGit(work, "commit", "-m", "init")
	runGit(work, "push", "origin", "HEAD:main")

	return work, bare
}

func TestWorkerSession_Start(t *testing.T) {
	work, _ := initGitRepo(t)

	w := &WorkerSession{
		Feature:   "fix-login-bug",
		AgentName: "copilot",
		RepoDir:   work,
	}

	b, err := w.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if b.Feature != "fix-login-bug" {
		t.Errorf("Feature = %q", b.Feature)
	}
	if b.AgentName != "copilot" {
		t.Errorf("AgentName = %q", b.AgentName)
	}
	if b.SessionID == "" {
		t.Error("SessionID is empty")
	}
	if !strings.HasPrefix(b.String(), "agent/fix-login-bug/copilot/") {
		t.Errorf("branch name format wrong: %q", b.String())
	}

	// Confirm we are on the new branch
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = work
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	current := strings.TrimSpace(string(out))
	if current != b.String() {
		t.Errorf("current branch = %q, want %q", current, b.String())
	}
}

func TestWorkerSession_Complete(t *testing.T) {
	work, bare := initGitRepo(t)

	w := &WorkerSession{
		Feature:   "add-api-tests",
		AgentName: "claude",
		RepoDir:   work,
	}

	if _, err := w.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := w.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Confirm the branch exists in the bare remote
	cmd := exec.Command("git", "branch", "-r")
	cmd.Dir = work
	out, _ := cmd.Output()
	if !strings.Contains(string(out), "add-api-tests") {
		t.Errorf("branch not found in remote after Complete: %s", out)
	}
	_ = bare
}

func TestWorkerSession_Abort(t *testing.T) {
	work, _ := initGitRepo(t)

	w := &WorkerSession{
		Feature:   "refactor-db",
		AgentName: "codex",
		RepoDir:   work,
	}

	b, err := w.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	branchName := b.String()

	if err := w.Abort(context.Background(), "test abort"); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	// Confirm branch is gone
	cmd := exec.Command("git", "branch")
	cmd.Dir = work
	out, _ := cmd.Output()
	if strings.Contains(string(out), branchName) {
		t.Errorf("branch %q still exists after Abort", branchName)
	}
}

func TestWorkerSession_RetryDetection(t *testing.T) {
	work, bare := initGitRepo(t)
	_ = bare

	// First session
	w1 := &WorkerSession{
		Feature:   "fix-login-bug",
		AgentName: "copilot",
		RepoDir:   work,
	}
	if _, err := w1.Start(context.Background()); err != nil {
		t.Fatalf("Start w1: %v", err)
	}
	if err := w1.Complete(context.Background()); err != nil {
		t.Fatalf("Complete w1: %v", err)
	}

	// Fetch so the second session can see the first branch
	cmd := exec.Command("git", "fetch", "origin")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	// Second session with retry detector
	lister := GitBranchLister(work)
	d := NewDetector(lister)
	w2 := &WorkerSession{
		Feature:   "fix-login-bug",
		AgentName: "copilot",
		RepoDir:   work,
	}
	w2.SetDetector(d)

	// Go back to main before starting second session
	checkout := exec.Command("git", "checkout", "main")
	checkout.Dir = work
	if out, err := checkout.CombinedOutput(); err != nil {
		t.Fatalf("checkout main: %v\n%s", err, out)
	}

	b2, err := w2.Start(context.Background())
	if err != nil {
		t.Fatalf("Start w2: %v", err)
	}
	if b2.SessionID == w1.branch.SessionID {
		t.Error("second session got same session ID as first")
	}
}

func TestWorkerSession_AbortBeforeStart(t *testing.T) {
	w := &WorkerSession{
		Feature:   "feat",
		AgentName: "copilot",
		RepoDir:   t.TempDir(),
	}
	// Should not return an error
	if err := w.Abort(context.Background(), "premature abort"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
