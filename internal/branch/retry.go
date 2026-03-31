package branch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// BranchLister returns a list of remote branch refs.
type BranchLister func(ctx context.Context) ([]string, error)

// GitBranchLister returns a BranchLister that calls git in repoDir to list
// remote branches matching the agent/* glob.
func GitBranchLister(repoDir string) BranchLister {
	return func(ctx context.Context) ([]string, error) {
		cmd := exec.CommandContext(ctx, "git", "branch", "-r", "--list", "origin/"+Prefix+"*")
		cmd.Dir = repoDir
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("branch: git branch -r: %w", err)
		}
		var refs []string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Strip "origin/" prefix added by git branch -r
			line = strings.TrimPrefix(line, "origin/")
			refs = append(refs, line)
		}
		return refs, nil
	}
}

// Detector uses a BranchLister to find existing agent sessions.
type Detector struct {
	lister BranchLister
}

// NewDetector creates a Detector backed by the provided BranchLister.
func NewDetector(lister BranchLister) *Detector {
	return &Detector{lister: lister}
}

// FindRetries returns all BranchNames matching the given feature and agentName.
func (d *Detector) FindRetries(ctx context.Context, feature, agentName string) ([]BranchName, error) {
	refs, err := d.lister(ctx)
	if err != nil {
		return nil, err
	}

	feature = Sanitize(feature)
	agentName = Sanitize(agentName)
	prefix := Prefix + feature + "/" + agentName + "/"

	var matches []BranchName
	for _, ref := range refs {
		if !strings.HasPrefix(ref, prefix) {
			continue
		}
		b, err := Parse(ref)
		if err != nil {
			continue
		}
		matches = append(matches, b)
	}
	return matches, nil
}

// IsRetry returns whether any previous sessions exist for this feature+agent combo.
// The attempt count is the number of prior sessions (0 means first attempt).
func (d *Detector) IsRetry(ctx context.Context, feature, agentName string) (bool, int, error) {
	matches, err := d.FindRetries(ctx, feature, agentName)
	if err != nil {
		return false, 0, err
	}
	count := len(matches)
	return count > 0, count, nil
}

// LatestSession returns the last BranchName in the list, or nil if none exist.
// Order is determined by the branch lister (typically alphabetical by session ID).
func (d *Detector) LatestSession(ctx context.Context, feature, agentName string) (*BranchName, error) {
	matches, err := d.FindRetries(ctx, feature, agentName)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	last := matches[len(matches)-1]
	return &last, nil
}
