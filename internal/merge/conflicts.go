package merge

import (
	"context"
	"log/slog"
)

// DetectOverlaps performs pre-merge analysis to identify files modified by
// multiple worker branches. Overlaps are warnings, not errors: sequential
// merge can succeed even when branches touch the same file (as long as the
// edits don't conflict at the line level).
//
// Branches with non-"completed" status are excluded from the analysis.
func DetectOverlaps(ctx context.Context, git GitOps, baseBranch string, branches []WorkerBranch, logger *slog.Logger) []OverlapInfo {
	// file -> branches that changed it
	fileBranches := make(map[string][]string)

	for _, wb := range branches {
		if wb.Status != "completed" {
			continue
		}
		files, err := git.ChangedFiles(ctx, wb.Branch, baseBranch)
		if err != nil {
			logger.Warn("merge: failed to get changed files for overlap detection",
				"branch", wb.Branch,
				"error", err,
			)
			continue
		}
		for _, f := range files {
			fileBranches[f] = append(fileBranches[f], wb.Branch)
		}
	}

	var overlaps []OverlapInfo
	for file, branches := range fileBranches {
		if len(branches) > 1 {
			overlaps = append(overlaps, OverlapInfo{
				File:     file,
				Branches: branches,
			})
		}
	}
	return overlaps
}
