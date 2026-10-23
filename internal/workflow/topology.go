package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Node returns a copy of the node with id.
func (t *Template) Node(id string) (Node, bool) {
	if t == nil {
		return Node{}, false
	}
	for _, n := range t.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// EntryNodes returns node ids with no incoming edges, except cycle-return edges
// into loop nodes, in sorted order.
func (t *Template) EntryNodes() []string {
	if t == nil {
		return nil
	}
	incoming := make(map[string]bool, len(t.Nodes))
	for _, edge := range t.Edges {
		if !t.IsCycleReturnEdge(edge) {
			incoming[edge.To.Node] = true
		}
	}
	entries := make([]string, 0)
	for _, node := range t.Nodes {
		if !incoming[node.ID] {
			entries = append(entries, node.ID)
		}
	}
	sort.Strings(entries)
	return entries
}

// CycleReturnEdges returns incoming edges that close a body path back to the
// loop node's in port, sorted by edge id.
func (t *Template) CycleReturnEdges(loopID string) []Edge {
	var edges []Edge
	if t == nil {
		return edges
	}
	for _, edge := range t.IncomingEdges(loopID) {
		if t.IsCycleReturnEdge(edge) {
			edges = append(edges, edge)
		}
	}
	return edges
}

// IsCycleReturnEdge reports whether e targets a loop's in port from a node
// reachable by following that loop's body path without revisiting the loop.
func (t *Template) IsCycleReturnEdge(e Edge) bool {
	if t == nil || e.To.Port != "in" || e.From.Node == e.To.Node {
		return false
	}
	found := false
	for _, edge := range t.Edges {
		if edge == e {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	loop, ok := t.Node(e.To.Node)
	if !ok || loop.Kind != NodeLoop {
		return false
	}
	seen := map[string]bool{loop.ID: true}
	stack := make([]string, 0)
	for _, edge := range t.OutgoingEdges(loop.ID) {
		if edge.From.Port == "body" && !seen[edge.To.Node] {
			stack = append(stack, edge.To.Node)
		}
	}
	for len(stack) != 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		if id == e.From.Node {
			return true
		}
		for _, edge := range t.OutgoingEdges(id) {
			if !seen[edge.To.Node] {
				stack = append(stack, edge.To.Node)
			}
		}
	}
	return false
}

// Successors returns the distinct downstream node ids in sorted order.
func (t *Template) Successors(id string) []string {
	set := make(map[string]struct{})
	if t != nil {
		for _, edge := range t.Edges {
			if edge.From.Node == id {
				set[edge.To.Node] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}

// Predecessors returns the distinct upstream node ids in sorted order.
func (t *Template) Predecessors(id string) []string {
	set := make(map[string]struct{})
	if t != nil {
		for _, edge := range t.Edges {
			if edge.To.Node == id {
				set[edge.From.Node] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}

// Descendants returns every node reachable downstream from id, excluding id.
func (t *Template) Descendants(id string) []string { return reachable(id, t.Successors) }

// Ancestors returns every node reachable upstream from id, excluding id.
func (t *Template) Ancestors(id string) []string { return reachable(id, t.Predecessors) }

func reachable(id string, adjacent func(string) []string) []string {
	seen := map[string]bool{id: true}
	stack := append([]string(nil), adjacent(id)...)
	for len(stack) != 0 {
		next := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[next] {
			continue
		}
		seen[next] = true
		stack = append(stack, adjacent(next)...)
	}
	delete(seen, id)
	return sortedKeys(seen)
}

// FindCycles returns one ordered representative cycle for each DFS back edge.
// Each cycle includes its starting node again at the end.
func (t *Template) FindCycles() [][]string {
	if t == nil {
		return nil
	}
	ids := make([]string, 0, len(t.Nodes))
	for _, node := range t.Nodes {
		ids = append(ids, node.ID)
	}
	sort.Strings(ids)
	state := make(map[string]uint8, len(ids))
	path := make([]string, 0, len(ids))
	pathIndex := make(map[string]int, len(ids))
	cycles := make([][]string, 0)
	var visit func(string)
	visit = func(id string) {
		state[id] = 1
		pathIndex[id] = len(path)
		path = append(path, id)
		for _, next := range t.Successors(id) {
			switch state[next] {
			case 0:
				visit(next)
			case 1:
				start := pathIndex[next]
				cycle := append([]string(nil), path[start:]...)
				cycle = append(cycle, next)
				cycles = append(cycles, cycle)
			}
		}
		path = path[:len(path)-1]
		delete(pathIndex, id)
		state[id] = 2
	}
	for _, id := range ids {
		if state[id] == 0 {
			visit(id)
		}
	}
	return cycles
}

// CycleBudgetOK reports whether a cycle passes through a loop node declaring
// max_iterations >= 1 and explains the first qualifying loop, if any.
func (t *Template) CycleBudgetOK(cycle []string) (bool, string) {
	for _, id := range cycle {
		node, ok := t.Node(id)
		if !ok || node.Kind != NodeLoop {
			continue
		}
		var cfg map[string]json.RawMessage
		if json.Unmarshal(node.Config, &cfg) != nil || cfg == nil {
			continue
		}
		var limit int
		if raw, exists := cfg["max_iterations"]; exists && json.Unmarshal(raw, &limit) == nil && limit >= 1 {
			return true, fmt.Sprintf("loop %s has max_iterations=%d", id, limit)
		}
	}
	return false, "no loop node with an explicit positive max_iterations"
}

// IncomingEdges returns all edges terminating at id in edge-id order.
func (t *Template) IncomingEdges(id string) []Edge {
	var edges []Edge
	if t != nil {
		for _, edge := range t.Edges {
			if edge.To.Node == id {
				edges = append(edges, edge)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return edges
}

// OutgoingEdges returns all edges starting at id in edge-id order.
func (t *Template) OutgoingEdges(id string) []Edge {
	var edges []Edge
	if t != nil {
		for _, edge := range t.Edges {
			if edge.From.Node == id {
				edges = append(edges, edge)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return edges
}
