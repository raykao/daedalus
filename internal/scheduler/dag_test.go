package scheduler_test

import (
	"testing"

	"github.com/raykao/agent-forge/internal/scheduler"
)

// ---------------------------------------------------------------------------
// AddNode / AddEdge basics
// ---------------------------------------------------------------------------

func TestDAG_AddNode_Single(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	if d.Size() != 1 {
		t.Fatalf("expected Size 1, got %d", d.Size())
	}
	if d.Remaining() != 1 {
		t.Fatalf("expected Remaining 1, got %d", d.Remaining())
	}
}

func TestDAG_AddNode_Idempotent(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("A")
	if d.Size() != 1 {
		t.Fatalf("expected Size 1 after duplicate AddNode, got %d", d.Size())
	}
}

func TestDAG_AddEdge_Basic(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	if err := d.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A->B: unexpected error: %v", err)
	}
}

func TestDAG_AddEdge_SelfLoop(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	if err := d.AddEdge("A", "A"); err == nil {
		t.Fatal("expected error for self-loop A->A, got nil")
	}
}

func TestDAG_AddEdge_MissingFromNode(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("B")
	if err := d.AddEdge("missing", "B"); err == nil {
		t.Fatal("expected error for edge from unknown node, got nil")
	}
}

func TestDAG_AddEdge_MissingToNode(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	if err := d.AddEdge("A", "missing"); err == nil {
		t.Fatal("expected error for edge to unknown node, got nil")
	}
}

func TestDAG_AddEdge_DirectCycle(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	if err := d.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A->B: %v", err)
	}
	if err := d.AddEdge("B", "A"); err == nil {
		t.Fatal("expected cycle error for B->A after A->B, got nil")
	}
}

func TestDAG_AddEdge_TransitiveCycle(t *testing.T) {
	// A -> B -> C -> A  (should fail on third edge that closes the cycle)
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	d.AddNode("C")
	if err := d.AddEdge("A", "B"); err != nil {
		t.Fatalf("AddEdge A->B: %v", err)
	}
	if err := d.AddEdge("B", "C"); err != nil {
		t.Fatalf("AddEdge B->C: %v", err)
	}
	if err := d.AddEdge("C", "A"); err == nil {
		t.Fatal("expected cycle error for C->A, got nil")
	}
}

// ---------------------------------------------------------------------------
// Ready()
// ---------------------------------------------------------------------------

func TestDAG_Ready_NoEdges(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	ready := d.Ready()
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready nodes, got %d: %v", len(ready), ready)
	}
}

func TestDAG_Ready_WithDependency(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	d.AddEdge("A", "B") //nolint:errcheck
	ready := d.Ready()
	if len(ready) != 1 || ready[0] != "A" {
		t.Fatalf("expected [A] ready, got %v", ready)
	}
}

func TestDAG_Ready_Sorted(t *testing.T) {
	d := scheduler.NewDAG()
	for _, id := range []string{"C", "A", "B"} {
		d.AddNode(id)
	}
	ready := d.Ready()
	if len(ready) != 3 {
		t.Fatalf("expected 3 ready, got %d", len(ready))
	}
	if ready[0] != "A" || ready[1] != "B" || ready[2] != "C" {
		t.Fatalf("expected sorted [A B C], got %v", ready)
	}
}

// ---------------------------------------------------------------------------
// Complete()
// ---------------------------------------------------------------------------

func TestDAG_Complete_ReleasesDependent(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	d.AddEdge("A", "B") //nolint:errcheck

	newlyReady := d.Complete("A")
	if len(newlyReady) != 1 || newlyReady[0] != "B" {
		t.Fatalf("expected Complete(A) to return [B], got %v", newlyReady)
	}
	if d.Remaining() != 1 {
		t.Fatalf("expected Remaining 1 after completing A, got %d", d.Remaining())
	}
	ready := d.Ready()
	if len(ready) != 1 || ready[0] != "B" {
		t.Fatalf("expected Ready [B], got %v", ready)
	}
}

func TestDAG_Complete_Idempotent(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	d.AddEdge("A", "B") //nolint:errcheck

	d.Complete("A")
	second := d.Complete("A") // should be no-op
	if second != nil {
		t.Fatalf("expected nil from second Complete, got %v", second)
	}
	if d.Remaining() != 1 {
		t.Fatalf("expected Remaining 1, got %d", d.Remaining())
	}
}

func TestDAG_Complete_MultiPredecessor_NotReadyUntilBothDone(t *testing.T) {
	// D depends on both B and C
	d := scheduler.NewDAG()
	for _, id := range []string{"B", "C", "D"} {
		d.AddNode(id)
	}
	d.AddEdge("B", "D") //nolint:errcheck
	d.AddEdge("C", "D") //nolint:errcheck

	newlyReady := d.Complete("B")
	if len(newlyReady) != 0 {
		t.Fatalf("D should not be ready after only B completes; got %v", newlyReady)
	}
	newlyReady = d.Complete("C")
	if len(newlyReady) != 1 || newlyReady[0] != "D" {
		t.Fatalf("D should become ready after C completes; got %v", newlyReady)
	}
}

// ---------------------------------------------------------------------------
// Remaining()
// ---------------------------------------------------------------------------

func TestDAG_Remaining_DecrementsOnComplete(t *testing.T) {
	d := scheduler.NewDAG()
	for _, id := range []string{"A", "B", "C"} {
		d.AddNode(id)
	}
	if d.Remaining() != 3 {
		t.Fatalf("expected 3, got %d", d.Remaining())
	}
	d.Complete("A")
	if d.Remaining() != 2 {
		t.Fatalf("expected 2, got %d", d.Remaining())
	}
	d.Complete("B")
	d.Complete("C")
	if d.Remaining() != 0 {
		t.Fatalf("expected 0, got %d", d.Remaining())
	}
}

// ---------------------------------------------------------------------------
// Validate()
// ---------------------------------------------------------------------------

func TestDAG_Validate_EmptyGraph(t *testing.T) {
	d := scheduler.NewDAG()
	if err := d.Validate(); err == nil {
		t.Fatal("expected error for empty graph, got nil")
	}
}

func TestDAG_Validate_NoCycle(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	d.AddEdge("A", "B") //nolint:errcheck
	if err := d.Validate(); err != nil {
		t.Fatalf("expected no error for valid DAG, got: %v", err)
	}
}

func TestDAG_Validate_SingleNode(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	if err := d.Validate(); err != nil {
		t.Fatalf("expected no error for single-node graph, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Diamond dependency pattern
// ---------------------------------------------------------------------------

func TestDAG_Diamond(t *testing.T) {
	// A -> {B, C} -> D
	d := scheduler.NewDAG()
	for _, id := range []string{"A", "B", "C", "D"} {
		d.AddNode(id)
	}
	d.AddEdge("A", "B") //nolint:errcheck
	d.AddEdge("A", "C") //nolint:errcheck
	d.AddEdge("B", "D") //nolint:errcheck
	d.AddEdge("C", "D") //nolint:errcheck

	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// Only A ready initially.
	ready := d.Ready()
	if len(ready) != 1 || ready[0] != "A" {
		t.Fatalf("expected [A] ready, got %v", ready)
	}

	// Complete A: B and C become ready.
	newlyReady := d.Complete("A")
	if len(newlyReady) != 2 {
		t.Fatalf("expected 2 newly ready after A, got %v", newlyReady)
	}
	ready = d.Ready()
	if len(ready) != 2 || ready[0] != "B" || ready[1] != "C" {
		t.Fatalf("expected [B C] ready, got %v", ready)
	}

	// Complete B: D still not ready (C pending).
	newlyReady = d.Complete("B")
	if len(newlyReady) != 0 {
		t.Fatalf("expected D not ready after only B, got %v", newlyReady)
	}

	// Complete C: D becomes ready.
	newlyReady = d.Complete("C")
	if len(newlyReady) != 1 || newlyReady[0] != "D" {
		t.Fatalf("expected [D] newly ready after C, got %v", newlyReady)
	}
	ready = d.Ready()
	if len(ready) != 1 || ready[0] != "D" {
		t.Fatalf("expected [D] ready, got %v", ready)
	}

	d.Complete("D")
	if d.Remaining() != 0 {
		t.Fatalf("expected Remaining 0, got %d", d.Remaining())
	}
}

// ---------------------------------------------------------------------------
// Linear chain
// ---------------------------------------------------------------------------

func TestDAG_LinearChain(t *testing.T) {
	// A -> B -> C
	d := scheduler.NewDAG()
	for _, id := range []string{"A", "B", "C"} {
		d.AddNode(id)
	}
	d.AddEdge("A", "B") //nolint:errcheck
	d.AddEdge("B", "C") //nolint:errcheck

	if err := d.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	steps := []string{"A", "B", "C"}
	for _, id := range steps {
		ready := d.Ready()
		if len(ready) != 1 || ready[0] != id {
			t.Fatalf("expected [%s] ready, got %v", id, ready)
		}
		d.Complete(id)
	}
	if d.Remaining() != 0 {
		t.Fatalf("expected 0 remaining, got %d", d.Remaining())
	}
}

// ---------------------------------------------------------------------------
// Dependents()
// ---------------------------------------------------------------------------

func TestDAG_Dependents(t *testing.T) {
	d := scheduler.NewDAG()
	d.AddNode("A")
	d.AddNode("B")
	d.AddNode("C")
	d.AddEdge("A", "B") //nolint:errcheck
	d.AddEdge("A", "C") //nolint:errcheck

	deps := d.Dependents("A")
	if len(deps) != 2 {
		t.Fatalf("expected 2 dependents of A, got %v", deps)
	}
	if len(d.Dependents("B")) != 0 {
		t.Fatalf("B should have no dependents")
	}
}
