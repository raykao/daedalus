// Package scheduler provides DAG-based dependency-ordered task scheduling.
// Tasks with no unmet dependencies run in parallel; dependent tasks wait for
// predecessors to complete.
package scheduler

import (
	"errors"
	"fmt"
	"sort"
)

// DFS color constants for cycle detection (3-color marking).
const (
	colorWhite = 0 // unvisited
	colorGray  = 1 // currently in the DFS call stack (back edge => cycle)
	colorBlack = 2 // fully explored
)

// DAG is a directed acyclic graph for tracking task dependencies.
// Edges point from a predecessor to its dependents (successor nodes).
// In-degree of a node equals the number of incomplete predecessors.
type DAG struct {
	nodes     map[string]bool     // set of all node IDs
	edges     map[string][]string // edges[from] = list of successor (dependent) IDs
	inDegree  map[string]int      // number of non-completed predecessors per node
	completed map[string]bool     // nodes that have been marked complete
}

// NewDAG returns an initialised, empty DAG.
func NewDAG() *DAG {
	return &DAG{
		nodes:     make(map[string]bool),
		edges:     make(map[string][]string),
		inDegree:  make(map[string]int),
		completed: make(map[string]bool),
	}
}

// AddNode registers a task node with the given ID.
// Calling AddNode with an ID that already exists is a no-op.
func (d *DAG) AddNode(id string) {
	if d.nodes[id] {
		return
	}
	d.nodes[id] = true
	if _, ok := d.inDegree[id]; !ok {
		d.inDegree[id] = 0
	}
}

// AddEdge adds a directed edge meaning "from must complete before to".
// Both nodes must already exist. Returns an error if:
//   - from == to (self-loop)
//   - either node is not registered
//   - adding the edge would create a cycle
func (d *DAG) AddEdge(from, to string) error {
	if from == to {
		return fmt.Errorf("dag: self-loop on node %q", from)
	}
	if !d.nodes[from] {
		return fmt.Errorf("dag: node %q not found", from)
	}
	if !d.nodes[to] {
		return fmt.Errorf("dag: node %q not found", to)
	}

	// A cycle would be created if 'from' is already reachable from 'to'.
	if d.reachable(to, from) {
		return fmt.Errorf("dag: adding edge %q -> %q would create a cycle", from, to)
	}

	d.edges[from] = append(d.edges[from], to)
	d.inDegree[to]++
	return nil
}

// Ready returns all nodes that have no unmet dependencies (in-degree == 0)
// and have not yet been completed. The result is sorted for determinism.
func (d *DAG) Ready() []string {
	var result []string
	for id := range d.nodes {
		if !d.completed[id] && d.inDegree[id] == 0 {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

// Complete marks node id as done, decrements the in-degree of all its
// successors, and returns the set of successor IDs whose in-degree just
// reached 0 (newly ready). Calling Complete on an already-completed node
// is a no-op and returns nil.
func (d *DAG) Complete(id string) []string {
	if d.completed[id] {
		return nil
	}
	d.completed[id] = true

	var newlyReady []string
	for _, succ := range d.edges[id] {
		d.inDegree[succ]--
		if d.inDegree[succ] == 0 && !d.completed[succ] {
			newlyReady = append(newlyReady, succ)
		}
	}
	return newlyReady
}

// Validate performs a full correctness check:
//   - returns an error for an empty graph
//   - runs a DFS with 3-color marking to detect any cycles
func (d *DAG) Validate() error {
	if len(d.nodes) == 0 {
		return errors.New("dag: graph is empty")
	}

	color := make(map[string]int, len(d.nodes))

	var dfs func(id string) error
	dfs = func(id string) error {
		color[id] = colorGray
		for _, succ := range d.edges[id] {
			switch color[succ] {
			case colorGray:
				return fmt.Errorf("dag: cycle detected involving node %q", succ)
			case colorWhite:
				if err := dfs(succ); err != nil {
					return err
				}
			}
			// colorBlack: fully explored, no cycle through this node
		}
		color[id] = colorBlack
		return nil
	}

	for id := range d.nodes {
		if color[id] == colorWhite {
			if err := dfs(id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Size returns the total number of nodes in the graph.
func (d *DAG) Size() int {
	return len(d.nodes)
}

// Remaining returns the number of nodes that have not yet been completed.
func (d *DAG) Remaining() int {
	return len(d.nodes) - len(d.completed)
}

// Dependents returns the direct successor IDs of node id (nodes that depend
// on id and become closer to ready when id completes). The returned slice is
// a copy; callers may modify it freely.
func (d *DAG) Dependents(id string) []string {
	succs := d.edges[id]
	if len(succs) == 0 {
		return nil
	}
	out := make([]string, len(succs))
	copy(out, succs)
	return out
}

// reachable returns true if target is reachable from start by following edges.
func (d *DAG) reachable(start, target string) bool {
	if start == target {
		return true
	}
	visited := make(map[string]bool)
	stack := []string{start}
	for len(stack) > 0 {
		curr := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[curr] {
			continue
		}
		visited[curr] = true
		for _, next := range d.edges[curr] {
			if next == target {
				return true
			}
			stack = append(stack, next)
		}
	}
	return false
}
