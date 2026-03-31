package merge

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// PRCreator abstracts pull request creation for testability.
type PRCreator interface {
	CreatePR(ctx context.Context, opts PROptions, head, base string) (*PRResult, error)
}

// GHCLIPRCreator creates pull requests using the gh CLI.
// This keeps the dependency footprint small and is consistent with the
// existing codebase pattern of shelling out to CLI tools.
type GHCLIPRCreator struct {
	RepoDir string // Working directory for gh commands
}

// ghPRJSON matches the subset of gh pr create --json output we need.
type ghPRJSON struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func (c *GHCLIPRCreator) CreatePR(ctx context.Context, opts PROptions, head, base string) (*PRResult, error) {
	args := []string{
		"pr", "create",
		"--repo", opts.Owner + "/" + opts.Repo,
		"--head", head,
		"--base", base,
		"--title", opts.Title,
		"--body", opts.Body,
	}

	if opts.Draft {
		args = append(args, "--draft")
	}

	for _, label := range opts.Labels {
		args = append(args, "--label", label)
	}

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = c.RepoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh pr create: %w\n%s", err, string(out))
	}

	// gh pr create prints the PR URL on success
	url := strings.TrimSpace(string(out))

	// Fetch PR number via gh pr view
	pr, err := c.viewPR(ctx, opts.Owner, opts.Repo, url)
	if err != nil {
		// Fallback: return URL without number
		return &PRResult{URL: url}, nil
	}

	return pr, nil
}

func (c *GHCLIPRCreator) viewPR(ctx context.Context, owner, repo, prURL string) (*PRResult, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view",
		"--repo", owner+"/"+repo,
		"--json", "number,url",
		prURL,
	)
	cmd.Dir = c.RepoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh pr view: %w\n%s", err, string(out))
	}

	var result ghPRJSON
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse gh pr view output: %w", err)
	}

	return &PRResult{
		Number: result.Number,
		URL:    result.URL,
	}, nil
}
