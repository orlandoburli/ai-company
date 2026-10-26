package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/orlandoburli/ai-company/internal/markers"
	"github.com/orlandoburli/ai-company/internal/memory"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type agentMarkerResult struct {
	Output      map[string]any
	HasOutput   bool
	Memories    []markers.Memories
	Publish     string
	Dropped     []string
	Failure     string
	FailureText string
}

// inspectAgentMarkers parses agent output and applies the node's declared
// output contract. It only prepares data; writes happen after terminal success.
func (e *Engine) inspectAgentMarkers(ctx context.Context, sc tenancy.Scope, run Run, node Node, output string) agentMarkerResult {
	var parsed agentMarkerResult
	outputBlock, found, err := markers.ParseOutput(output)
	if err != nil {
		parsed.Failure, parsed.FailureText = string(FailureCapability), err.Error()
		return parsed
	}
	if found {
		parsed.HasOutput = true
		declared := decodeConfig(node)["outputs"]
		var specs map[string]json.RawMessage
		_ = json.Unmarshal(declared, &specs)
		parsed.Output = make(map[string]any)
		for key, value := range outputBlock.Values {
			spec, ok := specs[key]
			if !ok {
				parsed.Dropped = append(parsed.Dropped, "AIC_OUTPUT:"+key+" (undeclared key)")
				continue
			}
			if !matchesOutputType(value, spec) {
				parsed.Dropped = append(parsed.Dropped, "AIC_OUTPUT:"+key+" (declared type mismatch)")
				continue
			}
			parsed.Output[key] = value
		}
	}

	memories, err := markers.ParseMemories(output)
	if err != nil {
		parsed.Dropped = append(parsed.Dropped, "AIC_MEMORIZE (invalid block: "+err.Error()+")")
	} else {
		for _, proposal := range memories {
			scope, ok := e.markerMemoryScope(ctx, sc, run, node, proposal.Scope)
			if !ok {
				parsed.Dropped = append(parsed.Dropped, fmt.Sprintf("AIC_MEMORIZE:%s (scope %q is not allowed)", proposal.Key, proposal.Scope))
				continue
			}
			if e.memory == nil {
				parsed.Dropped = append(parsed.Dropped, "AIC_MEMORIZE:"+proposal.Key+" (memory store unavailable)")
				continue
			}
			entries, _, readErr := e.memory.Read(ctx, sc, scope)
			if readErr != nil {
				e.logger.Warn("check marker memory author", "run", run.ID, "node", node.ID, "key", proposal.Key, "error", readErr)
				parsed.Dropped = append(parsed.Dropped, "AIC_MEMORIZE:"+proposal.Key+" (could not verify current author)")
				continue
			}
			for _, entry := range entries {
				if entry.Key == proposal.Key && entry.Source == memory.SourceHuman {
					parsed.Dropped = append(parsed.Dropped, "AIC_MEMORIZE:"+proposal.Key+" (human-authored memory is protected)")
					scope = memory.Scope{}
					break
				}
			}
			if scope.Ref != "" {
				parsed.Memories = append(parsed.Memories, proposal)
			}
		}
	}

	if strings.Contains(output, "AIC_SPAWN") {
		parsed.Dropped = append(parsed.Dropped, "agent-output:AIC_SPAWN (not enabled)")
	}
	if strings.Contains(output, "AIC_PUBLISH") {
		proposal, ok := markers.FindPublish(output)
		if !ok {
			parsed.Failure, parsed.FailureText = string(FailureCapability), "malformed AIC_PUBLISH proposal"
			return parsed
		}
		parsed.Publish = proposal
		if !publishCapability(node) {
			parsed.Failure, parsed.FailureText = string(FailureCapability), "AIC_PUBLISH requires a declared publish capability"
		}
	}
	return parsed
}

func (e *Engine) markerMemoryScope(ctx context.Context, sc tenancy.Scope, run Run, node Node, scopeName string) (memory.Scope, bool) {
	switch scopeName {
	case "agent":
		ref := stringConfig(decodeConfig(node), "agent_id")
		if ref == "" {
			return memory.Scope{}, false
		}
		return memory.Scope{Kind: memory.ScopeAgent, Ref: ref}, true
	case "workspace":
		if e.work == nil || run.ItemID == "" {
			return memory.Scope{}, false
		}
		item, err := e.work.GetItem(ctx, sc, run.ItemID)
		if err != nil {
			return memory.Scope{}, false
		}
		productID, _, _ := e.productForItem(ctx, sc, item)
		if productID == "" {
			return memory.Scope{}, false
		}
		return memory.Scope{Kind: memory.ScopeWorkspace, Ref: productID}, true
	default:
		return memory.Scope{}, false
	}
}

func (e *Engine) writeAgentMarkers(ctx context.Context, sc tenancy.Scope, run Run, step StepRun, node Node, markersOut agentMarkerResult) {
	if markersOut.Failure != "" {
		return
	}
	if e.memory == nil {
		if markersOut.HasOutput || len(markersOut.Memories) > 0 {
			e.logger.Warn("agent markers have no memory store", "run", run.ID, "node", node.ID)
		}
		return
	}
	if markersOut.HasOutput {
		for _, key := range sortedKeys(markersOut.Output) {
			value := markersOut.Output[key]
			encoded, err := json.Marshal(value)
			if err != nil {
				e.logger.Warn("encode AIC_OUTPUT memory", "run", run.ID, "node", node.ID, "key", key, "error", err)
				continue
			}
			content := strings.TrimSpace(string(encoded))
			if text, ok := value.(string); ok {
				content = text
			}
			if _, err := e.memory.Write(ctx, sc, memory.Write{
				Scope: memory.Scope{Kind: memory.ScopeRun, Ref: run.ID}, Key: key,
				ContentMD: content, ContentJSON: encoded, Source: memory.SourceStepOutput,
				SourceRunID: run.ID, SourceStepRunID: step.ID,
			}); err != nil {
				e.logger.Warn("write AIC_OUTPUT run memory", "run", run.ID, "node", node.ID, "key", key, "error", err)
			}
		}
	}
	for _, proposal := range markersOut.Memories {
		scope, ok := e.markerMemoryScope(ctx, sc, run, node, proposal.Scope)
		if !ok {
			// A scope can disappear between authorization and write; report it as a failed best-effort write.
			e.logger.Warn("write AIC_MEMORIZE memory", "run", run.ID, "node", node.ID, "key", proposal.Key, "error", "scope no longer available")
			continue
		}
		if _, err := e.memory.Write(ctx, sc, memory.Write{
			Scope: scope, Key: proposal.Key, ContentMD: proposal.Content,
			Source: memory.SourceAgentMarker, SourceRunID: run.ID, SourceStepRunID: step.ID,
			Tags: []string{"author_agent:" + stringConfig(decodeConfig(node), "agent_id")},
		}); err != nil {
			e.logger.Warn("write AIC_MEMORIZE memory", "run", run.ID, "node", node.ID, "key", proposal.Key, "error", err)
		}
	}
}

func publishCapability(node Node) bool {
	var config struct {
		Capabilities struct {
			Publish bool `json:"publish"`
		} `json:"capabilities"`
	}
	_ = json.Unmarshal(node.Config, &config)
	return config.Capabilities.Publish
}

func matchesOutputType(value any, raw json.RawMessage) bool {
	var typeName string
	if json.Unmarshal(raw, &typeName) != nil {
		var schema struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &schema) != nil {
			return false
		}
		typeName = schema.Type
	}
	switch typeName {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		switch value.(type) {
		case json.Number, float64, float32, int, int64:
			return true
		}
	case "integer":
		switch n := value.(type) {
		case json.Number:
			_, err := n.Int64()
			return err == nil
		case float64:
			return n == float64(int64(n))
		case int, int64:
			return true
		}
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	}
	return false
}
