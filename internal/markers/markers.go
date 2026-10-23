// Package markers parses the plain-text proposal markers emitted by agents.
// Parsing only returns data; callers remain responsible for authorization and
// checkpoint-gated execution.
package markers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Output is a parsed AIC_OUTPUT block: the keys an agent claims it produced.
type Output struct {
	Values map[string]any
}

// Memories is a parsed AIC_MEMORIZE block.
type Memories struct {
	Key     string `json:"key"`
	Content string `json:"content"`
	Scope   string `json:"scope"` // agent | workspace; callers must refuse other scopes.
}

// ParseOutput returns the first AIC_OUTPUT block, if present. Its body must be
// exactly one JSON object (optionally wrapped in a markdown code fence).
func ParseOutput(text string) (Output, bool, error) {
	blocks, err := parseBlocks(text, "AIC_OUTPUT", "AIC_OUTPUT_END")
	if err != nil {
		return Output{}, false, err
	}
	if len(blocks) == 0 {
		return Output{}, false, nil
	}
	if len(blocks) > 1 {
		return Output{}, true, fmt.Errorf("multiple AIC_OUTPUT blocks")
	}
	var value map[string]any
	if err := decodeStrictJSON(blocks[0], &value); err != nil {
		return Output{}, true, fmt.Errorf("invalid AIC_OUTPUT JSON: %w", err)
	}
	if value == nil {
		return Output{}, true, fmt.Errorf("AIC_OUTPUT must contain a JSON object")
	}
	return Output{Values: value}, true, nil
}

// ParseMemories returns every AIC_MEMORIZE block. Scope validation is left to
// the caller so unauthorized proposals can be reported rather than hidden.
func ParseMemories(text string) ([]Memories, error) {
	blocks, err := parseBlocks(text, "AIC_MEMORIZE", "AIC_MEMORIZE_END")
	if err != nil {
		return nil, err
	}
	out := make([]Memories, 0, len(blocks))
	for i, block := range blocks {
		var value Memories
		if err := decodeStrictJSON(block, &value); err != nil {
			return nil, fmt.Errorf("invalid AIC_MEMORIZE block %d JSON: %w", i+1, err)
		}
		if strings.TrimSpace(value.Key) == "" || value.Content == "" || strings.TrimSpace(value.Scope) == "" {
			return nil, fmt.Errorf("AIC_MEMORIZE block %d requires key, content, and scope", i+1)
		}
		out = append(out, value)
	}
	return out, nil
}

// FindPublish returns the text of the first AIC_PUBLISH proposal. It never
// executes or authorizes that proposal.
func FindPublish(text string) (string, bool) {
	blocks, err := parseBlocks(text, "AIC_PUBLISH", "AIC_PUBLISH_END")
	if err != nil || len(blocks) == 0 {
		return "", false
	}
	return strings.TrimSpace(blocks[0]), true
}

func parseBlocks(text, start, end string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != start {
			continue
		}
		var body []string
		foundEnd := false
		for i++; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == end {
				foundEnd = true
				break
			}
			body = append(body, lines[i])
		}
		if !foundEnd {
			return nil, fmt.Errorf("%s block is missing %s fence", start, end)
		}
		blocks = append(blocks, stripFence(strings.Join(body, "\n")))
	}
	return blocks, nil
}

func stripFence(body string) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) >= 2 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") && strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[1 : len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func decodeStrictJSON(text string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(text))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
