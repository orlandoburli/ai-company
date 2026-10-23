// Package workflow models and validates workflow template graphs.
package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// NodeKind identifies the execution semantics of a graph node.
type NodeKind string

const (
	NodeAgent       NodeKind = "agent"
	NodeIntegration NodeKind = "integration"
	NodeCheckpoint  NodeKind = "checkpoint"
	NodeLoop        NodeKind = "loop"
	NodeMerge       NodeKind = "merge"
)

// OnError selects how a node failure affects its workflow branch.
type OnError string

const (
	OnErrorStop            OnError = "stop_workflow"
	OnErrorContinueRegular OnError = "continue_regular_output"
	OnErrorContinueError   OnError = "continue_error_output"
)

// WorkspaceKind selects the workspace policy for a node.
type WorkspaceKind string

const (
	WorkspaceScratch  WorkspaceKind = "scratch"
	WorkspaceWorktree WorkspaceKind = "worktree"
	WorkspaceDir      WorkspaceKind = "dir"
)

// Position is a node's optional canvas location; it has no execution meaning.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// PortRef identifies a named port on a node.
type PortRef struct {
	Node string `json:"node"`
	Port string `json:"port"`
}

// MemoryTier selects a memory mode or an explicit list of keys.
// Mode is empty when Keys contains an explicit key list.
type MemoryTier struct {
	Mode MemoryMode `json:"-"`
	Keys []string   `json:"-"`
}

// MemoryMode selects all, summarized, no, or automatic memory retrieval.
type MemoryMode string

const (
	MemoryFull    MemoryMode = "full"
	MemorySummary MemoryMode = "summary"
	MemoryNone    MemoryMode = "none"
	MemoryAuto    MemoryMode = "auto"
)

// UnmarshalJSON accepts a mode string or an explicit array of memory keys.
func (t *MemoryTier) UnmarshalJSON(data []byte) error {
	var mode string
	if err := json.Unmarshal(data, &mode); err == nil {
		t.Mode = MemoryMode(mode)
		t.Keys = nil
		return nil
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil || keys == nil {
		return fmt.Errorf("memory tier must be a mode string or an array of keys")
	}
	t.Mode = ""
	t.Keys = keys
	return nil
}

// MarshalJSON writes a memory tier as the design's string-or-array shape.
func (t MemoryTier) MarshalJSON() ([]byte, error) {
	if t.Mode != "" {
		return json.Marshal(t.Mode)
	}
	return json.Marshal(t.Keys)
}

// MemorySelector chooses memory independently for each scope.
type MemorySelector struct {
	Run       MemoryTier `json:"run"`
	Agent     MemoryTier `json:"agent"`
	Workspace MemoryTier `json:"workspace"`
}

// Node is one execution step in a workflow template.
type Node struct {
	ID               string          `json:"id"`
	Kind             NodeKind        `json:"kind"`
	Name             string          `json:"name"`
	Type             string          `json:"type"`
	TypeVersion      int             `json:"type_version"`
	Config           json.RawMessage `json:"config"`
	Position         *Position       `json:"position,omitempty"`
	OnError          OnError         `json:"on_error"`
	AlwaysOutputData bool            `json:"always_output_data"`
	Workspace        WorkspaceKind   `json:"workspace"`
	Memory           *MemorySelector `json:"memory,omitempty"`
}

// Edge connects a named output port to a named input port.
type Edge struct {
	ID   string  `json:"id"`
	From PortRef `json:"from"`
	To   PortRef `json:"to"`
}

// Template is a versioned workflow graph.
type Template struct {
	SchemaVersion int    `json:"schema_version"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
}

// Validation error codes are stable identifiers for callers and tests.
const (
	CodeSchemaVersion      = "schema_version"
	CodeEmptyGraph         = "empty_graph"
	CodeDuplicateNodeID    = "duplicate_node_id"
	CodeDuplicateEdgeID    = "duplicate_edge_id"
	CodeUnknownNode        = "unknown_node"
	CodeInvalidPort        = "invalid_port"
	CodeInvalidKind        = "invalid_kind"
	CodeInvalidOnError     = "invalid_on_error"
	CodeInvalidWorkspace   = "invalid_workspace"
	CodeNoEntryNode        = "no_entry_node"
	CodeUnreachableNode    = "unreachable_node"
	CodeCycleWithoutBudget = "cycle_without_budget"
	CodeMergeBudget        = "merge_budget"
	CodeInvalidOutputType  = "invalid_output_type"
	CodeInvalidConfig      = "invalid_config"
	CodeDuplicateEdgePair  = "duplicate_edge_pair"
	CodeInvalidNodeID      = "invalid_node_id"
	CodeInvalidEdgeID      = "invalid_edge_id"
	CodeInvalidNodeType    = "invalid_node_type"
	CodeInvalidMemory      = "invalid_memory"
)

// ValidationError describes one invalid part of a template.
type ValidationError struct {
	Code    string
	Message string
}

// Error implements error.
func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

func validationError(code, format string, args ...any) error {
	return &ValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ParseTemplate strictly decodes a JSON object and validates the resulting graph.
func ParseTemplate(data []byte) (*Template, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, fmt.Errorf("decode workflow template: %w", err)
	}
	if shape == nil {
		return nil, fmt.Errorf("workflow template must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var t Template
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("decode workflow template: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("decode workflow template: multiple JSON values")
		}
		return nil, fmt.Errorf("decode workflow template: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// PortsFor returns the declared input and output ports for a node kind.
func PortsFor(kind NodeKind) (inputs []string, outputs []string) {
	switch kind {
	case NodeAgent, NodeIntegration, NodeMerge:
		return []string{"in"}, []string{"main", "error"}
	case NodeCheckpoint:
		return []string{"in"}, []string{"approved", "rejected"}
	case NodeLoop:
		return []string{"in"}, []string{"body", "done"}
	default:
		return nil, nil
	}
}

// Validate checks schema, node and edge shapes, reachability, ports and config.
func (t *Template) Validate() error {
	if t == nil || t.SchemaVersion != 1 {
		version := 0
		if t != nil {
			version = t.SchemaVersion
		}
		return validationError(CodeSchemaVersion, "schema_version must be 1 (got %d)", version)
	}
	if len(t.Nodes) == 0 {
		return validationError(CodeEmptyGraph, "workflow graph has no nodes")
	}

	nodes := make(map[string]Node, len(t.Nodes))
	for i := range t.Nodes {
		n := &t.Nodes[i]
		if n.ID == "" {
			return validationError(CodeInvalidNodeID, "node id must not be empty")
		}
		if _, exists := nodes[n.ID]; exists {
			return validationError(CodeDuplicateNodeID, "duplicate node id %q", n.ID)
		}
		if _, outputs := PortsFor(n.Kind); outputs == nil {
			return validationError(CodeInvalidKind, "node %q has unknown kind %q", n.ID, n.Kind)
		}
		if n.OnError != OnErrorStop && n.OnError != OnErrorContinueRegular && n.OnError != OnErrorContinueError {
			// on_error is intentionally explicit: unlike workspace, it has no default.
			return validationError(CodeInvalidOnError, "node %q has invalid on_error %q", n.ID, n.OnError)
		}
		if n.Workspace == "" {
			n.Workspace = WorkspaceScratch
		}
		if n.Workspace != WorkspaceScratch && n.Workspace != WorkspaceWorktree && n.Workspace != WorkspaceDir {
			return validationError(CodeInvalidWorkspace, "node %q has invalid workspace %q", n.ID, n.Workspace)
		}
		if strings.TrimSpace(n.Type) == "" || n.TypeVersion < 1 {
			return validationError(CodeInvalidNodeType, "node %q requires a non-empty type and type_version >= 1", n.ID)
		}
		if n.Memory != nil {
			for tierName, tier := range map[string]MemoryTier{"run": n.Memory.Run, "agent": n.Memory.Agent, "workspace": n.Memory.Workspace} {
				if tier.Mode != "" && tier.Mode != MemoryFull && tier.Mode != MemorySummary && tier.Mode != MemoryNone && tier.Mode != MemoryAuto {
					return validationError(CodeInvalidMemory, "node %q memory.%s has invalid mode %q", n.ID, tierName, tier.Mode)
				}
			}
		}
		nodes[n.ID] = *n
	}
	edgeIDs := make(map[string]struct{}, len(t.Edges))
	pairs := make(map[string]struct{}, len(t.Edges))
	for _, e := range t.Edges {
		if e.ID == "" {
			return validationError(CodeInvalidEdgeID, "edge id must not be empty")
		}
		if _, exists := edgeIDs[e.ID]; exists {
			return validationError(CodeDuplicateEdgeID, "duplicate edge id %q", e.ID)
		}
		edgeIDs[e.ID] = struct{}{}
		from, fromOK := nodes[e.From.Node]
		to, toOK := nodes[e.To.Node]
		if !fromOK || !toOK {
			missing := e.From.Node
			if fromOK {
				missing = e.To.Node
			}
			return validationError(CodeUnknownNode, "edge %q references unknown node %q", e.ID, missing)
		}
		_, outs := PortsFor(from.Kind)
		if !contains(outs, e.From.Port) {
			return validationError(CodeInvalidPort, "edge %q source %s.%s is not an output port", e.ID, e.From.Node, e.From.Port)
		}
		ins, _ := PortsFor(to.Kind)
		if !contains(ins, e.To.Port) {
			return validationError(CodeInvalidPort, "edge %q target %s.%s is not an input port", e.ID, e.To.Node, e.To.Port)
		}
		pair := e.From.Node + "\x00" + e.From.Port + "\x00" + e.To.Node + "\x00" + e.To.Port
		if _, exists := pairs[pair]; exists {
			return validationError(CodeDuplicateEdgePair, "duplicate edge from %s.%s to %s.%s", e.From.Node, e.From.Port, e.To.Node, e.To.Port)
		}
		pairs[pair] = struct{}{}
	}

	entries := t.EntryNodes()
	if len(entries) == 0 {
		return validationError(CodeNoEntryNode, "workflow graph has no entry node")
	}
	visited := make(map[string]bool, len(nodes))
	stack := append([]string(nil), entries...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[id] {
			continue
		}
		visited[id] = true
		stack = append(stack, t.Successors(id)...)
	}
	// The nodes are walked in the graph's own order, not over the map: the
	// first unreachable node must be the same one every time, so the
	// validation panel highlights the same offender and the server's verdict
	// does not change between two identical requests.
	for i := range t.Nodes {
		id := t.Nodes[i].ID
		if !visited[id] {
			return validationError(CodeUnreachableNode, "node %q is unreachable from every entry node", id)
		}
	}

	for _, cycle := range t.FindCycles() {
		if ok, reason := t.CycleBudgetOK(cycle); !ok {
			return validationError(CodeCycleWithoutBudget, "cycle %s has no loop with max_iterations >= 1 (%s)", strings.Join(cycle, " -> "), reason)
		}
	}
	incomingCounts := make(map[string]int, len(nodes))
	for _, edge := range t.Edges {
		incomingCounts[edge.To.Node]++
	}
	for _, n := range t.Nodes {
		cfg, err := nodeConfig(n)
		if err != nil {
			return err
		}
		if err := validateNodeConfig(n, cfg, nodes, incomingCounts[n.ID]); err != nil {
			return err
		}
	}
	return nil
}

func nodeConfig(n Node) (map[string]json.RawMessage, error) {
	data := bytes.TrimSpace(n.Config)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		data = []byte("{}")
	}
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(data, &cfg); err != nil || cfg == nil {
		return nil, validationError(CodeInvalidConfig, "node %q config must be a JSON object", n.ID)
	}
	return cfg, nil
}

func validateNodeConfig(n Node, cfg map[string]json.RawMessage, known map[string]Node, incoming int) error {
	readString := func(key string) (string, bool) {
		var value string
		raw, ok := cfg[key]
		if !ok || json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		return value, true
	}
	switch n.Kind {
	case NodeAgent:
		agentID, ok := readString("agent_id")
		if !ok || strings.TrimSpace(agentID) == "" {
			return validationError(CodeInvalidConfig, "agent node %q requires a non-empty agent_id", n.ID)
		}
		if raw, exists := cfg["outputs"]; exists {
			var outputs map[string]json.RawMessage
			if err := json.Unmarshal(raw, &outputs); err != nil || outputs == nil {
				return validationError(CodeInvalidOutputType, "agent node %q outputs must be an object", n.ID)
			}
			for key, value := range outputs {
				var typeName string
				if json.Unmarshal(value, &typeName) == nil {
					switch typeName {
					case "string", "number", "integer", "boolean", "object", "array":
						continue
					default:
						return validationError(CodeInvalidOutputType, "agent node %q output %q has unsupported type %q", n.ID, key, typeName)
					}
				}
				var schema map[string]json.RawMessage
				if err := json.Unmarshal(value, &schema); err != nil || schema == nil {
					return validationError(CodeInvalidOutputType, "agent node %q output %q must be a supported JSON type or schema object", n.ID, key)
				}
			}
		}
	case NodeMerge:
		mode, ok := readString("mode")
		if !ok || (mode != "all" && mode != "any" && mode != "n_of_m") {
			return validationError(CodeInvalidConfig, "merge node %q config.mode must be all, any, or n_of_m", n.ID)
		}
		if mode == "n_of_m" {
			var count int
			if raw, exists := cfg["n"]; !exists || json.Unmarshal(raw, &count) != nil || count < 1 {
				return validationError(CodeMergeBudget, "merge node %q n_of_m requires n >= 1", n.ID)
			}
			if count > incoming {
				return validationError(CodeMergeBudget, "merge node %q requires n <= incoming edge count (%d)", n.ID, incoming)
			}
		}
	case NodeCheckpoint:
		policy, ok := readString("policy")
		if !ok || (policy != "retry_step" && policy != "abort" && policy != "goto_step") {
			return validationError(CodeInvalidConfig, "checkpoint node %q config.policy must be retry_step, abort, or goto_step", n.ID)
		}
		if policy == "goto_step" {
			target, ok := readString("target")
			if !ok || strings.TrimSpace(target) == "" {
				return validationError(CodeInvalidConfig, "checkpoint node %q goto_step requires a target", n.ID)
			}
			if _, exists := known[target]; !exists {
				return validationError(CodeInvalidConfig, "checkpoint node %q goto_step target %q does not exist", n.ID, target)
			}
		}
	case NodeLoop:
		var limit int
		if raw, exists := cfg["max_iterations"]; !exists || json.Unmarshal(raw, &limit) != nil || limit < 1 {
			return validationError(CodeInvalidConfig, "loop node %q requires max_iterations >= 1", n.ID)
		}
		until, exists := cfg["until"]
		if !exists {
			return validationError(CodeInvalidConfig, "loop node %q requires an until condition", n.ID)
		}
		var boolean bool
		if json.Unmarshal(until, &boolean) == nil {
			break
		}
		var expression string
		if json.Unmarshal(until, &expression) != nil || strings.TrimSpace(expression) == "" {
			return validationError(CodeInvalidConfig, "loop node %q until must be a boolean or non-empty expression", n.ID)
		}
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
