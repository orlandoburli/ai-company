package workflow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Problem is one thing wrong with a graph.
type Problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	NodeID  string `json:"node_id,omitempty"`
	EdgeID  string `json:"edge_id,omitempty"`
}

// ValidateAll reports every problem with the graph, not just the first.
//
// Validate remains the source of truth for validation rules and messages. We
// repeatedly ask it for its first error, record that error, and minimally
// repair a private copy so later errors can be observed. FindCycles reports
// one representative per DFS back edge, so cycle diagnostics are limited to
// the cycles that existing validation exposes.
func ValidateAll(t *Template) []Problem {
	if t == nil {
		return []Problem{{Code: CodeSchemaVersion, Message: "schema_version must be 1 (got 0)"}}
	}
	work := cloneTemplate(t)
	var problems []Problem
	for attempts := 0; attempts < 1000; attempts++ {
		err := work.Validate()
		if err == nil {
			return problems
		}
		validation, ok := err.(*ValidationError)
		if !ok {
			return problems
		}
		problem := Problem{Code: validation.Code, Message: validation.Message}
		problem.NodeID, problem.EdgeID = problemLocation(work, validation)
		problems = append(problems, problem)
		if !repairValidationError(work, validation, problem) {
			// A defensive guard: if an unrecognized validation code is added,
			// still return the errors the shared validator can currently find.
			return problems
		}
	}
	return problems
}

func cloneTemplate(t *Template) *Template {
	copy := *t
	copy.Nodes = append([]Node(nil), t.Nodes...)
	copy.Edges = append([]Edge(nil), t.Edges...)
	for i := range copy.Nodes {
		copy.Nodes[i].Config = append([]byte(nil), t.Nodes[i].Config...)
		if t.Nodes[i].Memory != nil {
			memory := *t.Nodes[i].Memory
			memory.Run.Keys = append([]string(nil), memory.Run.Keys...)
			memory.Agent.Keys = append([]string(nil), memory.Agent.Keys...)
			memory.Workspace.Keys = append([]string(nil), memory.Workspace.Keys...)
			copy.Nodes[i].Memory = &memory
		}
	}
	return &copy
}

func problemLocation(t *Template, validation *ValidationError) (nodeID, edgeID string) {
	code, message := validation.Code, validation.Message
	switch code {
	case CodeInvalidEdgeID, CodeDuplicateEdgeID, CodeUnknownNode, CodeInvalidPort, CodeDuplicateEdgePair:
		for _, edge := range t.Edges {
			if strings.Contains(message, fmt.Sprintf("edge %q", edge.ID)) ||
				(code == CodeDuplicateEdgePair && strings.Contains(message, edge.From.Node+"."+edge.From.Port+" to "+edge.To.Node+"."+edge.To.Port)) {
				edgeID = edge.ID
				break
			}
		}
	case CodeInvalidNodeID:
		// An empty node id cannot be represented as an ID in the response.
	case CodeNoEntryNode, CodeEmptyGraph, CodeSchemaVersion:
		return "", ""
	case CodeUnreachableEndNode:
		// The message names the end node first and the start node second:
		// the problem is about the end node.
		if id := quotedAfter(message, "end node "); id != "" {
			nodeID = id
		}
	case CodeUnexpectedEntryNode:
		// The message names the node that was left without an incoming edge.
		if id := quotedAfter(message, "node "); id != "" {
			nodeID = id
		}
	default:
		for _, node := range t.Nodes {
			if node.ID != "" && strings.Contains(message, fmt.Sprintf("%q", node.ID)) {
				nodeID = node.ID
				break
			}
		}
	}
	return nodeID, edgeID
}

func repairValidationError(t *Template, validation *ValidationError, problem Problem) bool {
	switch validation.Code {
	case CodeSchemaVersion:
		t.SchemaVersion = 1
		return true
	case CodeEmptyGraph:
		return false
	case CodeInvalidNodeID:
		for i := range t.Nodes {
			if t.Nodes[i].ID == "" {
				t.Nodes[i].ID = uniqueNodeID(t, "validation-node")
				return true
			}
		}
	case CodeDuplicateNodeID:
		for i := range t.Nodes {
			if t.Nodes[i].ID == problem.NodeID {
				for j := i + 1; j < len(t.Nodes); j++ {
					if t.Nodes[j].ID == problem.NodeID {
						t.Nodes[j].ID = uniqueNodeID(t, "validation-node")
						return true
					}
				}
			}
		}
	case CodeInvalidKind, CodeInvalidOnError, CodeInvalidWorkspace, CodeInvalidNodeType, CodeInvalidMemory:
		if i := nodeIndex(t, problem.NodeID); i >= 0 {
			node := &t.Nodes[i]
			switch validation.Code {
			case CodeInvalidKind:
				node.Kind = NodeAgent
			case CodeInvalidOnError:
				node.OnError = OnErrorStop
			case CodeInvalidWorkspace:
				node.Workspace = WorkspaceScratch
			case CodeInvalidNodeType:
				node.Type, node.TypeVersion = "validation", 1
			case CodeInvalidMemory:
				if node.Memory == nil {
					return false
				}
				if strings.Contains(validation.Message, "memory.run") {
					node.Memory.Run = MemoryTier{Mode: MemoryFull}
				} else if strings.Contains(validation.Message, "memory.agent") {
					node.Memory.Agent = MemoryTier{Mode: MemoryFull}
				} else {
					node.Memory.Workspace = MemoryTier{Mode: MemoryFull}
				}
			}
			return true
		}
	case CodeInvalidEdgeID:
		for i := range t.Edges {
			if t.Edges[i].ID == "" {
				t.Edges[i].ID = uniqueEdgeID(t, "validation-edge")
				return true
			}
		}
	case CodeDuplicateEdgeID:
		for i := range t.Edges {
			if t.Edges[i].ID == problem.EdgeID {
				for j := i + 1; j < len(t.Edges); j++ {
					if t.Edges[j].ID == problem.EdgeID {
						t.Edges[j].ID = uniqueEdgeID(t, "validation-edge")
						return true
					}
				}
			}
		}
	case CodeUnknownNode, CodeInvalidPort, CodeDuplicateEdgePair:
		return removeEdge(t, problem.EdgeID)
	case CodeNoEntryNode:
		if len(t.Edges) != 0 {
			t.Edges = t.Edges[1:]
			return true
		}
		return false
	case CodeUnreachableNode:
		if nodeIndex(t, problem.NodeID) < 0 {
			return false
		}
		entries := t.EntryNodes()
		if len(entries) == 0 {
			return false
		}
		from, ok := t.Node(entries[0])
		if !ok {
			return false
		}
		_, outputs := PortsFor(from.Kind)
		if len(outputs) == 0 {
			return false
		}
		t.Edges = append(t.Edges, Edge{ID: uniqueEdgeID(t, "validation-link"), From: PortRef{Node: from.ID, Port: outputs[0]}, To: PortRef{Node: problem.NodeID, Port: "in"}})
		return true
	case CodeDuplicateStartNode:
		// A graph has one start: the second one is not the start.
		return removeNode(t, problem.NodeID)
	case CodeDuplicateEndNode:
		// A graph has one end: the second one is not the end.
		return removeNode(t, problem.NodeID)
	case CodeStartNodeEdges:
		return repairStartEdges(t, problem.NodeID)
	case CodeEndNodeEdges:
		return repairEndEdges(t, problem.NodeID)
	case CodeUnexpectedEntryNode:
		return repairUnexpectedEntry(t, problem.NodeID)
	case CodeUnreachableEndNode:
		return repairUnreachableEnd(t, problem.NodeID)
	case CodeCycleWithoutBudget:
		cycles := t.FindCycles()
		for _, cycle := range cycles {
			if ok, _ := t.CycleBudgetOK(cycle); !ok {
				for i := 0; i+1 < len(cycle); i++ {
					for _, edge := range t.Edges {
						if edge.From.Node == cycle[i] && edge.To.Node == cycle[i+1] {
							return removeEdge(t, edge.ID)
						}
					}
				}
			}
		}
	case CodeMergeBudget:
		if i := nodeIndex(t, problem.NodeID); i >= 0 {
			return setConfigValue(&t.Nodes[i], "mode", `"any"`)
		}
	case CodeInvalidConfig, CodeInvalidOutputType:
		if i := nodeIndex(t, problem.NodeID); i >= 0 {
			return repairNodeConfig(&t.Nodes[i], validation)
		}
	}
	return false
}

func repairNodeConfig(node *Node, validation *ValidationError) bool {
	if validation.Code == CodeInvalidOutputType {
		cfg := readConfig(node.Config)
		outputsRaw := cfg["outputs"]
		var outputs map[string]json.RawMessage
		if json.Unmarshal(outputsRaw, &outputs) != nil || outputs == nil {
			cfg["outputs"] = json.RawMessage(`{}`)
		} else {
			key := quotedAfter(validation.Message, "output ")
			if key == "" {
				return false
			}
			outputs[key] = json.RawMessage(`"string"`)
			encoded, err := json.Marshal(outputs)
			if err != nil {
				return false
			}
			cfg["outputs"] = encoded
		}
		return writeConfig(node, cfg)
	}

	cfg := readConfig(node.Config)
	if cfg == nil {
		cfg = make(map[string]json.RawMessage)
	}
	switch node.Kind {
	case NodeAgent:
		cfg["agent_id"] = json.RawMessage(`"validation"`)
	case NodeMerge:
		cfg["mode"] = json.RawMessage(`"any"`)
	case NodeCheckpoint:
		if strings.Contains(validation.Message, "requires a target") {
			target, ok := existingNodeID(node)
			if !ok {
				return false
			}
			encoded, _ := json.Marshal(target)
			cfg["target"] = encoded
		} else if strings.Contains(validation.Message, "target") && strings.Contains(validation.Message, "does not exist") {
			target, ok := existingNodeID(node)
			if !ok {
				return false
			}
			encoded, _ := json.Marshal(target)
			cfg["target"] = encoded
		} else {
			cfg["policy"] = json.RawMessage(`"abort"`)
		}
	case NodeLoop:
		if strings.Contains(validation.Message, "max_iterations") {
			cfg["max_iterations"] = json.RawMessage(`1`)
		} else {
			cfg["until"] = json.RawMessage(`true`)
		}
	default:
		return false
	}
	return writeConfig(node, cfg)
}

func readConfig(raw []byte) map[string]json.RawMessage {
	var cfg map[string]json.RawMessage
	if json.Unmarshal(raw, &cfg) != nil || cfg == nil {
		return nil
	}
	return cfg
}

func writeConfig(node *Node, cfg map[string]json.RawMessage) bool {
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return false
	}
	node.Config = encoded
	return true
}

func setConfigValue(node *Node, key, value string) bool {
	cfg := readConfig(node.Config)
	if cfg == nil {
		cfg = make(map[string]json.RawMessage)
	}
	cfg[key] = json.RawMessage(value)
	return writeConfig(node, cfg)
}

func quotedAfter(message, marker string) string {
	index := strings.Index(message, marker)
	if index < 0 {
		return ""
	}
	start := index + len(marker)
	var value string
	if _, err := fmt.Sscanf(message[start:], "%q", &value); err != nil {
		return ""
	}
	return value
}

func existingNodeID(node *Node) (string, bool) {
	// The checkpoint's config rule only needs an existing target. A temporary
	// self-target is still valid when this is the graph's sole node.
	if node.ID != "" {
		return node.ID, true
	}
	return "", false
}

func nodeIndex(t *Template, id string) int {
	for i := range t.Nodes {
		if t.Nodes[i].ID == id {
			return i
		}
	}
	return -1
}

// removeNode removes a node and every edge that touches it.
func removeNode(t *Template, id string) bool {
	index := nodeIndex(t, id)
	if index < 0 {
		return false
	}
	t.Nodes = append(t.Nodes[:index], t.Nodes[index+1:]...)
	kept := t.Edges[:0]
	for _, e := range t.Edges {
		if e.From.Node == id || e.To.Node == id {
			continue
		}
		kept = append(kept, e)
	}
	t.Edges = kept
	return true
}

// repairStartEdges makes the start node well-formed: no incoming edge and
// exactly one outgoing edge.
func repairStartEdges(t *Template, id string) bool {
	// The start takes no input.
	removed := false
	kept := t.Edges[:0]
	for _, e := range t.Edges {
		if e.To.Node == id {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	t.Edges = kept
	if removed {
		return true
	}
	outgoing := 0
	for _, e := range t.Edges {
		if e.From.Node == id {
			outgoing++
		}
	}
	if outgoing > 1 {
		// One successor, no more: the first edge stays.
		seen := false
		kept = t.Edges[:0]
		for _, e := range t.Edges {
			if e.From.Node == id {
				if seen {
					continue
				}
				seen = true
			}
			kept = append(kept, e)
		}
		t.Edges = kept
		return true
	}
	if outgoing == 1 {
		return false
	}
	// No successor: the start hands the flow to the first node that takes
	// an input.
	for i := range t.Nodes {
		target := t.Nodes[i]
		if target.ID == id {
			continue
		}
		inputs, _ := PortsFor(target.Kind)
		if !contains(inputs, "in") || hasEdge(t, id, "main", target.ID, "in") {
			continue
		}
		appendEdge(t, id, "main", target.ID, "in")
		return true
	}
	return false
}

// repairEndEdges makes the end node well-formed: no outgoing edge and at
// least one incoming edge.
func repairEndEdges(t *Template, id string) bool {
	// The end has no output.
	removed := false
	kept := t.Edges[:0]
	for _, e := range t.Edges {
		if e.From.Node == id {
			removed = true
			continue
		}
		kept = append(kept, e)
	}
	t.Edges = kept
	if removed {
		return true
	}
	if len(t.IncomingEdges(id)) > 0 {
		return false
	}
	// The end needs an input. It is a sink, so an edge into it cannot close
	// a cycle.
	for i := len(t.Nodes) - 1; i >= 0; i-- {
		source := t.Nodes[i]
		if source.ID == id {
			continue
		}
		outputs, _ := PortsFor(source.Kind)
		if !contains(outputs, "main") || hasEdge(t, source.ID, "main", id, "in") {
			continue
		}
		appendEdge(t, source.ID, "main", id, "in")
		return true
	}
	return false
}

// repairUnexpectedEntry gives the node the flow left behind an incoming edge,
// so the start node stays the only entry.
func repairUnexpectedEntry(t *Template, id string) bool {
	if nodeIndex(t, id) < 0 {
		return false
	}
	reachable := t.reachableNodes()
	descendants := map[string]bool{}
	for _, descendant := range t.Descendants(id) {
		descendants[descendant] = true
	}
	// The start node is the natural source: it has one successor to give.
	start, hasStart := t.StartNode()
	if hasStart && len(t.OutgoingEdges(start)) == 0 && !descendants[start] {
		appendEdge(t, start, "main", id, "in")
		return true
	}
	for i := range t.Nodes {
		source := t.Nodes[i]
		if source.ID == id || descendants[source.ID] || !reachable[source.ID] {
			continue
		}
		outputs, _ := PortsFor(source.Kind)
		if !contains(outputs, "main") || hasEdge(t, source.ID, "main", id, "in") {
			continue
		}
		appendEdge(t, source.ID, "main", id, "in")
		return true
	}
	return false
}

// repairUnreachableEnd connects the end node to the flow it never reached.
func repairUnreachableEnd(t *Template, id string) bool {
	if nodeIndex(t, id) < 0 {
		return false
	}
	reachable := t.reachableNodes()
	// The start node has exactly one successor: it is not the source here.
	start, _ := t.StartNode()
	for i := range t.Nodes {
		source := t.Nodes[i]
		if source.ID == id || source.ID == start || !reachable[source.ID] {
			continue
		}
		outputs, _ := PortsFor(source.Kind)
		if !contains(outputs, "main") || hasEdge(t, source.ID, "main", id, "in") {
			continue
		}
		appendEdge(t, source.ID, "main", id, "in")
		return true
	}
	return false
}

// reachableNodes returns the nodes the flow reaches from its entry nodes.
func (t *Template) reachableNodes() map[string]bool {
	visited := map[string]bool{}
	stack := append([]string(nil), t.EntryNodes()...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[id] {
			continue
		}
		visited[id] = true
		stack = append(stack, t.Successors(id)...)
	}
	return visited
}

// hasEdge reports whether the graph already connects the two ports.
func hasEdge(t *Template, from, fromPort, to, toPort string) bool {
	for _, e := range t.Edges {
		if e.From.Node == from && e.From.Port == fromPort && e.To.Node == to && e.To.Port == toPort {
			return true
		}
	}
	return false
}

// appendEdge adds an edge between the two ports with a fresh id.
func appendEdge(t *Template, from, fromPort, to, toPort string) {
	t.Edges = append(t.Edges, Edge{ID: uniqueEdgeID(t, "validation-link"),
		From: PortRef{Node: from, Port: fromPort}, To: PortRef{Node: to, Port: toPort}})
}

func removeEdge(t *Template, id string) bool {
	for i := range t.Edges {
		if t.Edges[i].ID == id {
			t.Edges = append(t.Edges[:i], t.Edges[i+1:]...)
			return true
		}
	}
	return false
}

func uniqueNodeID(t *Template, prefix string) string {
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d", prefix, n)
		if nodeIndex(t, candidate) < 0 {
			return candidate
		}
	}
}

func uniqueEdgeID(t *Template, prefix string) string {
	for n := 1; ; n++ {
		candidate := fmt.Sprintf("%s-%d", prefix, n)
		found := false
		for _, edge := range t.Edges {
			if edge.ID == candidate {
				found = true
				break
			}
		}
		if !found {
			return candidate
		}
	}
}
