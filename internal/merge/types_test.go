package merge

import "testing"

func TestMergeRequest_Validate(t *testing.T) {
	valid := MergeRequest{
		ContextID:    "ctx-test",
		BaseBranch:   "main",
		TargetBranch: "agent/feature/merged",
		RepoDir:      "/tmp/repo",
		WorkerBranches: []WorkerBranch{
			{Branch: "agent/feature/copilot/abc123", Status: "completed"},
		},
	}

	t.Run("valid request", func(t *testing.T) {
		if err := valid.Validate(); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("empty ContextID", func(t *testing.T) {
		r := valid
		r.ContextID = ""
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty ContextID")
		}
	})

	t.Run("empty BaseBranch", func(t *testing.T) {
		r := valid
		r.BaseBranch = ""
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty BaseBranch")
		}
	})

	t.Run("empty TargetBranch", func(t *testing.T) {
		r := valid
		r.TargetBranch = ""
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty TargetBranch")
		}
	})

	t.Run("BaseBranch equals TargetBranch", func(t *testing.T) {
		r := valid
		r.BaseBranch = "main"
		r.TargetBranch = "main"
		if err := r.Validate(); err == nil {
			t.Fatal("expected error when branches match")
		}
	})

	t.Run("empty RepoDir", func(t *testing.T) {
		r := valid
		r.RepoDir = ""
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty RepoDir")
		}
	})

	t.Run("no worker branches", func(t *testing.T) {
		r := valid
		r.WorkerBranches = nil
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty WorkerBranches")
		}
	})

	t.Run("worker branch with empty Branch", func(t *testing.T) {
		r := valid
		r.WorkerBranches = []WorkerBranch{{Branch: "", Status: "completed"}}
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty Branch")
		}
	})

	t.Run("PROptions with empty Owner", func(t *testing.T) {
		r := valid
		r.PROptions = &PROptions{Repo: "test"}
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty PROptions.Owner")
		}
	})

	t.Run("PROptions with empty Repo", func(t *testing.T) {
		r := valid
		r.PROptions = &PROptions{Owner: "test"}
		if err := r.Validate(); err == nil {
			t.Fatal("expected error for empty PROptions.Repo")
		}
	})

	t.Run("valid PROptions", func(t *testing.T) {
		r := valid
		r.PROptions = &PROptions{Owner: "raykao", Repo: "agent-forge"}
		if err := r.Validate(); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
}
