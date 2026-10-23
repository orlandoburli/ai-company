// Package memory stores tenant-scoped, revisioned memory entries.
package memory

import (
	"encoding/json"
	"errors"
	"time"
)

type ScopeKind string

const (
	ScopeRun       ScopeKind = "run"
	ScopeItem      ScopeKind = "item"
	ScopeAgent     ScopeKind = "agent"
	ScopeWorkspace ScopeKind = "workspace"
)

type Scope struct {
	Kind ScopeKind
	Ref  string
}

type Source string

const (
	SourceStepOutput   Source = "step_output"
	SourceCheckpoint   Source = "checkpoint"
	SourceIntervention Source = "intervention"
	SourceAgentMarker  Source = "agent_marker"
	SourceIntegration  Source = "integration"
	SourceHuman        Source = "human"
)

type Entry struct {
	ID              string
	TenantID        string
	Scope           Scope
	Key             string
	Revision        int
	ContentMD       string
	ContentJSON     json.RawMessage
	Source          Source
	SourceRunID     string
	SourceStepRunID string
	SourceUserID    string
	Tags            []string
	TokenEstimate   int
	Seq             int64
	SupersededBy    string
	CreatedAt       time.Time
}

type Write struct {
	Scope           Scope
	Key             string
	ContentMD       string
	ContentJSON     json.RawMessage
	Source          Source
	Tags            []string
	SourceRunID     string
	SourceStepRunID string
	SourceUserID    string
}

var (
	ErrInvalidScope     = errors.New("memory: invalid scope")
	ErrInvalidKey       = errors.New("memory: invalid key")
	ErrNotFound         = errors.New("memory: not found")
	ErrRunScopeReadonly = errors.New("memory: run scope is read-only for human writes")
)
