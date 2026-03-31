package merge

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// Merger combines worker branches into a single target branch using
// sequential merge. Each completed worker branch is merged in the order
// provided (which should match the scheduler's execution order so that
// dependency-ordered changes layer correctly).
//
// If a branch merge conflicts, the merge is aborted, the branch is
// recorded in Conflicts, and the Merger proceeds to the next branch.
// This "best-effort" approach maximizes the amount of work that lands
// in the final PR.
type Merger struct {
	git    GitOps
	pr     PRCreator
	logger *slog.Logger
}

// NewMerger creates a Merger with the given git backend and optional PR creator.
// If pr is nil, PR creation is skipped even if PROptions is set.
func NewMerger(git GitOps, pr PRCreator, logger *slog.Logger) *Merger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Merger{git: git, pr: pr, logger: logger}
}

// Merge performs the sequential merge and optional PR creation.
//
// Steps:
//  1. Validate the request.
//  2. Fetch latest remote state.
//  3. Detect file overlaps (advisory only).
//  4. Create target branch from base.
//  5. Sequentially merge each completed worker branch.
//  6. Push target branch.
//  7. Create PR if requested.
func (m *Merger) Merge(ctx context.Context, req MergeRequest) (*MergeResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	result := &MergeResult{
		ContextID:    req.ContextID,
		TargetBranch: req.TargetBranch,
	}

	// Step 2: fetch
	m.logger.Info("merge: fetching remote state", "repoDir", req.RepoDir)
	if err := m.git.Fetch(ctx); err != nil {
		return nil, fmt.Errorf("merge: fetch: %w", err)
	}

	// Step 3: pre-merge overlap detection
	result.FileOverlaps = DetectOverlaps(ctx, m.git, req.BaseBranch, req.WorkerBranches, m.logger)
	for _, ov := range result.FileOverlaps {
		m.logger.Warn("merge: file overlap detected",
			"file", ov.File,
			"branches", ov.Branches,
		)
	}

	// Step 4: create target branch from base
	m.logger.Info("merge: creating target branch",
		"target", req.TargetBranch,
		"base", req.BaseBranch,
	)
	baseRef := req.BaseBranch
	// Prefer remote-tracking base if available
	if m.git.BranchExists(ctx, "origin/"+req.BaseBranch) {
		baseRef = "origin/" + req.BaseBranch
	}
	if err := m.git.CreateBranch(ctx, req.TargetBranch, baseRef); err != nil {
		return nil, fmt.Errorf("merge: create target branch: %w", err)
	}

	// Step 5: sequential merge
	for _, wb := range req.WorkerBranches {
		if wb.Status != "completed" {
			result.Skipped = append(result.Skipped, SkipInfo{
				Branch: wb.Branch,
				Reason: fmt.Sprintf("task status is %q (not completed)", wb.Status),
			})
			m.logger.Info("merge: skipping non-completed branch",
				"branch", wb.Branch,
				"taskID", wb.TaskID,
				"status", wb.Status,
			)
			continue
		}

		if !m.git.BranchExists(ctx, wb.Branch) {
			result.Skipped = append(result.Skipped, SkipInfo{
				Branch: wb.Branch,
				Reason: "branch not found (local or remote)",
			})
			m.logger.Warn("merge: branch not found, skipping",
				"branch", wb.Branch,
				"taskID", wb.TaskID,
			)
			continue
		}

		msg := fmt.Sprintf("Merge %s (task %s, skill %s)", wb.Branch, wb.TaskID, wb.SkillID)
		m.logger.Info("merge: merging branch",
			"branch", wb.Branch,
			"taskID", wb.TaskID,
		)

		if err := m.git.MergeBranch(ctx, wb.Branch, msg); err != nil {
			// Merge conflict: record it, abort, continue.
			conflictFiles, cfErr := m.git.ConflictFiles(ctx)
			if cfErr != nil {
				m.logger.Warn("merge: failed to get conflict files",
					"branch", wb.Branch,
					"error", cfErr,
				)
			}

			result.Conflicts = append(result.Conflicts, ConflictInfo{
				Branch: wb.Branch,
				Files:  conflictFiles,
			})

			m.logger.Warn("merge: conflict detected, skipping branch",
				"branch", wb.Branch,
				"taskID", wb.TaskID,
				"conflictFiles", conflictFiles,
			)

			if abortErr := m.git.AbortMerge(ctx); abortErr != nil {
				return nil, fmt.Errorf("merge: failed to abort conflicted merge of %s: %w", wb.Branch, abortErr)
			}
			continue
		}

		result.MergedBranches = append(result.MergedBranches, wb.Branch)
		m.logger.Info("merge: branch merged successfully",
			"branch", wb.Branch,
			"taskID", wb.TaskID,
		)
	}

	// If nothing was merged, no point pushing or creating a PR.
	if len(result.MergedBranches) == 0 {
		m.logger.Warn("merge: no branches were merged",
			"contextID", req.ContextID,
			"skipped", len(result.Skipped),
			"conflicts", len(result.Conflicts),
		)
		return result, nil
	}

	// Step 6: record final SHA
	sha, err := m.git.HeadSHA(ctx)
	if err != nil {
		return nil, fmt.Errorf("merge: get HEAD SHA: %w", err)
	}
	result.CommitSHA = sha

	// Step 7: push
	m.logger.Info("merge: pushing target branch",
		"branch", req.TargetBranch,
		"sha", sha,
	)
	if err := m.git.Push(ctx, req.TargetBranch); err != nil {
		return nil, fmt.Errorf("merge: push: %w", err)
	}

	// Step 8: create PR
	if req.PROptions != nil && m.pr != nil {
		m.logger.Info("merge: creating pull request",
			"head", req.TargetBranch,
			"base", req.BaseBranch,
		)

		opts := *req.PROptions
		if opts.Title == "" {
			opts.Title = fmt.Sprintf("Agent Forge: merged %d worker branches (%s)",
				len(result.MergedBranches), req.ContextID)
		}
		if opts.Body == "" {
			opts.Body = m.defaultPRBody(req, result)
		}

		pr, prErr := m.pr.CreatePR(ctx, opts, req.TargetBranch, req.BaseBranch)
		if prErr != nil {
			m.logger.Error("merge: PR creation failed",
				"error", prErr,
			)
			// PR failure is not fatal: the branch is pushed.
			return result, fmt.Errorf("merge: PR creation: %w (branch %s was pushed successfully)", prErr, req.TargetBranch)
		}
		result.PR = pr
		m.logger.Info("merge: pull request created",
			"number", pr.Number,
			"url", pr.URL,
		)
	}

	return result, nil
}

func (m *Merger) defaultPRBody(req MergeRequest, result *MergeResult) string {
	return buildPRBody(req, result)
}

// buildPRBody generates a Markdown PR description summarising which
// branches were merged, skipped, conflicted, and which files overlapped.
func buildPRBody(req MergeRequest, result *MergeResult) string {
	body := fmt.Sprintf("## Agent Forge — Merged Worker Branches\n\n"+
		"**Context ID:** `%s`\n"+
		"**Base branch:** `%s`\n"+
		"**Merged branch:** `%s`\n\n",
		req.ContextID, req.BaseBranch, req.TargetBranch)

	body += fmt.Sprintf("### Merged (%d)\n\n", len(result.MergedBranches))
	for _, b := range result.MergedBranches {
		body += fmt.Sprintf("- ✅ `%s`\n", b)
	}

	if len(result.Skipped) > 0 {
		body += fmt.Sprintf("\n### Skipped (%d)\n\n", len(result.Skipped))
		for _, s := range result.Skipped {
			body += fmt.Sprintf("- ⏭️ `%s` — %s\n", s.Branch, s.Reason)
		}
	}

	if len(result.Conflicts) > 0 {
		body += fmt.Sprintf("\n### Conflicts (%d)\n\n", len(result.Conflicts))
		for _, c := range result.Conflicts {
			body += fmt.Sprintf("- ⚠️ `%s`", c.Branch)
			if len(c.Files) > 0 {
				body += fmt.Sprintf(" — files: %s", joinQuoted(c.Files))
			}
			body += "\n"
		}
	}

	if len(result.FileOverlaps) > 0 {
		body += "\n### File Overlaps (advisory)\n\n"
		for _, o := range result.FileOverlaps {
			body += fmt.Sprintf("- `%s` modified by: %s\n", o.File, joinQuoted(o.Branches))
		}
	}

	return body
}

func joinQuoted(ss []string) string {
	quoted := make([]string, len(ss))
	for i, s := range ss {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}
