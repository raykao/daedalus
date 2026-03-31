package merge

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitOps abstracts git operations so the Merger can be tested without
// shelling out to real git commands.
type GitOps interface {
	// Fetch updates remote tracking branches.
	Fetch(ctx context.Context) error

	// CreateBranch creates a new branch at startPoint and checks it out.
	CreateBranch(ctx context.Context, name, startPoint string) error

	// BranchExists reports whether a local or remote-tracking branch exists.
	BranchExists(ctx context.Context, branch string) bool

	// MergeBranch merges branch into the current HEAD with a merge commit.
	// Returns an error if the merge has conflicts.
	MergeBranch(ctx context.Context, branch, message string) error

	// AbortMerge aborts an in-progress merge.
	AbortMerge(ctx context.Context) error

	// ConflictFiles returns the list of files with unresolved conflicts.
	// Should only be called when MergeBranch returns an error.
	ConflictFiles(ctx context.Context) ([]string, error)

	// ChangedFiles returns files changed on branch relative to base.
	ChangedFiles(ctx context.Context, branch, base string) ([]string, error)

	// HeadSHA returns the SHA of the current HEAD.
	HeadSHA(ctx context.Context) (string, error)

	// Push pushes a branch to the remote.
	Push(ctx context.Context, branch string) error
}

// ExecGit implements GitOps by shelling out to the git CLI.
type ExecGit struct {
	RepoDir string
	Remote  string // defaults to "origin"
}

func (g *ExecGit) remote() string {
	if g.Remote != "" {
		return g.Remote
	}
	return "origin"
}

func (g *ExecGit) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.RepoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (g *ExecGit) Fetch(ctx context.Context) error {
	_, err := g.run(ctx, "fetch", g.remote(), "--prune")
	return err
}

func (g *ExecGit) CreateBranch(ctx context.Context, name, startPoint string) error {
	_, err := g.run(ctx, "checkout", "-b", name, startPoint)
	return err
}

func (g *ExecGit) BranchExists(ctx context.Context, branch string) bool {
	// Check local
	if _, err := g.run(ctx, "rev-parse", "--verify", branch); err == nil {
		return true
	}
	// Check remote tracking
	_, err := g.run(ctx, "rev-parse", "--verify", g.remote()+"/"+branch)
	return err == nil
}

func (g *ExecGit) MergeBranch(ctx context.Context, branch, message string) error {
	// Try remote-tracking ref first, fall back to local
	ref := g.remote() + "/" + branch
	if _, err := g.run(ctx, "rev-parse", "--verify", ref); err != nil {
		ref = branch
	}
	_, err := g.run(ctx, "merge", "--no-ff", "-m", message, ref)
	return err
}

func (g *ExecGit) AbortMerge(ctx context.Context) error {
	_, err := g.run(ctx, "merge", "--abort")
	return err
}

func (g *ExecGit) ConflictFiles(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (g *ExecGit) ChangedFiles(ctx context.Context, branch, base string) ([]string, error) {
	// Resolve refs: prefer remote-tracking, fall back to local
	branchRef := g.resolveRef(ctx, branch)
	baseRef := g.resolveRef(ctx, base)

	out, err := g.run(ctx, "diff", "--name-only", baseRef+"..."+branchRef)
	if err != nil {
		return nil, err
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

func (g *ExecGit) HeadSHA(ctx context.Context) (string, error) {
	return g.run(ctx, "rev-parse", "HEAD")
}

func (g *ExecGit) Push(ctx context.Context, branch string) error {
	_, err := g.run(ctx, "push", g.remote(), branch)
	return err
}

// resolveRef returns the remote-tracking ref if it exists, otherwise the branch name.
func (g *ExecGit) resolveRef(ctx context.Context, branch string) string {
	remote := g.remote() + "/" + branch
	if _, err := g.run(ctx, "rev-parse", "--verify", remote); err == nil {
		return remote
	}
	return branch
}
