package branch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os/exec"
)

// WorkerSession manages the git branch lifecycle for a single agent task execution.
type WorkerSession struct {
	Feature   string
	AgentName string
	RepoDir   string
	RemoteURL string // used for push; if empty, uses "origin"

	branch   *BranchName
	detector *Detector
}

// Start generates a session ID, optionally logs retry info, creates and checks out
// the branch, and returns the BranchName.
func (w *WorkerSession) Start(ctx context.Context) (BranchName, error) {
	sessionID, err := newSessionID()
	if err != nil {
		return BranchName{}, fmt.Errorf("branch: generate session ID: %w", err)
	}

	b, err := New(w.Feature, w.AgentName, sessionID)
	if err != nil {
		return BranchName{}, err
	}

	// Retry detection is best-effort: log but don't fail on error.
	if w.detector != nil {
		isRetry, count, detErr := w.detector.IsRetry(ctx, w.Feature, w.AgentName)
		if detErr != nil {
			slog.WarnContext(ctx, "branch: retry detection failed", "error", detErr)
		} else if isRetry {
			slog.InfoContext(ctx, "branch: retry detected",
				"feature", w.Feature,
				"agent", w.AgentName,
				"attempt", count+1,
				"prior_sessions", count,
			)
		}
	}

	if err := w.gitCheckoutBranch(ctx, b.String()); err != nil {
		return BranchName{}, fmt.Errorf("branch: checkout %q: %w", b.String(), err)
	}

	w.branch = &b
	slog.InfoContext(ctx, "branch: worker session started",
		"branch", b.String(),
		"feature", b.Feature,
		"agent", b.AgentName,
		"session_id", b.SessionID,
	)
	return b, nil
}

// Complete pushes the branch to the remote and logs completion.
func (w *WorkerSession) Complete(ctx context.Context) error {
	if w.branch == nil {
		return fmt.Errorf("branch: Complete called before Start")
	}
	remote := w.remote()
	cmd := exec.CommandContext(ctx, "git", "push", remote, w.branch.String())
	cmd.Dir = w.RepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("branch: push %q to %q: %w\n%s", w.branch.String(), remote, err, out)
	}
	slog.InfoContext(ctx, "branch: worker session complete",
		"branch", w.branch.String(),
		"remote", remote,
	)
	return nil
}

// Abort logs the reason and deletes the local branch.
func (w *WorkerSession) Abort(ctx context.Context, reason string) error {
	if w.branch == nil {
		slog.WarnContext(ctx, "branch: Abort called before Start", "reason", reason)
		return nil
	}
	slog.WarnContext(ctx, "branch: worker session aborted",
		"branch", w.branch.String(),
		"reason", reason,
	)
	// Switch away from the branch before deleting it.
	checkoutCmd := exec.CommandContext(ctx, "git", "checkout", "-")
	checkoutCmd.Dir = w.RepoDir
	_ = checkoutCmd.Run() // best-effort

	del := exec.CommandContext(ctx, "git", "branch", "-D", w.branch.String())
	del.Dir = w.RepoDir
	if out, err := del.CombinedOutput(); err != nil {
		return fmt.Errorf("branch: delete %q: %w\n%s", w.branch.String(), err, out)
	}
	return nil
}

// SetDetector injects a custom Detector (useful for testing).
func (w *WorkerSession) SetDetector(d *Detector) {
	w.detector = d
}

func (w *WorkerSession) remote() string {
	if w.RemoteURL != "" {
		return w.RemoteURL
	}
	return "origin"
}

func (w *WorkerSession) gitCheckoutBranch(ctx context.Context, branchName string) error {
	cmd := exec.CommandContext(ctx, "git", "checkout", "-b", branchName)
	cmd.Dir = w.RepoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// newSessionID generates a 16-byte random hex string.
func newSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
