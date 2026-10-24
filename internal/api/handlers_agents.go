package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
	"github.com/orlandoburli/ai-company/internal/runner"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// jobCanceller is the part of the worker the HTTP surface needs: stopping a
// running adapter after the job row has been moved to cancelled.
type jobCanceller interface {
	Cancel(jobID string)
}

// WithAgentWorker wires the worker's cancellation hook into the server.
func WithAgentWorker(w jobCanceller) Option { return func(s *Server) { s.agentWorker = w } }

// WithAgentRuntime shares the store and live-event hub with the worker, so the
// HTTP surface and the runtime observe the same jobs.
func WithAgentRuntime(store *agent.Store, hub *agent.Hub) Option {
	return func(s *Server) {
		if store != nil {
			s.agents = store
		}
		if hub != nil {
			s.agentHub = hub
		}
	}
}

// --- agent registry --------------------------------------------------------

// The agent is the WHAT — persona, role, skills, capabilities. Its transport
// (a CLI binary or an API model) moved to the runner registry, and the agent
// names an ordered chain of runners: the order the runtime tries them in.
type agentCreateRequest struct {
	Name             string          `json:"name"`
	Role             string          `json:"role"`
	Runners          []string        `json:"runners"`
	Capabilities     json.RawMessage `json:"capabilities"`
	ConcurrencyLimit int             `json:"concurrency_limit"`
	Enabled          *bool           `json:"enabled"`
	SoulMD           *string         `json:"soul_md"`
	CredentialRef    string          `json:"credential_ref"`
	SkillsPolicy     json.RawMessage `json:"skills_policy"`

	// Legacy transport fields. The transport moved to the runner chain, so
	// these are accepted and ignored: an old client keeps working, but the
	// agent it creates carries no transport until a runner is bound to it.
	Kind      json.RawMessage `json:"kind"`
	Config    json.RawMessage `json:"config"`
	Fallbacks json.RawMessage `json:"fallbacks"`
}

type agentPatchRequest struct {
	Name             *string         `json:"name"`
	Role             *string         `json:"role"`
	Runners          *[]string       `json:"runners"`
	Capabilities     json.RawMessage `json:"capabilities"`
	ConcurrencyLimit *int            `json:"concurrency_limit"`
	Enabled          *bool           `json:"enabled"`
	SoulMD           *string         `json:"soul_md"`
	CredentialRef    *string         `json:"credential_ref"`
	SkillsPolicy     json.RawMessage `json:"skills_policy"`

	// Legacy transport fields — accepted and ignored, see agentCreateRequest.
	Kind      json.RawMessage `json:"kind"`
	Config    json.RawMessage `json:"config"`
	Fallbacks json.RawMessage `json:"fallbacks"`
}

// agentResponse is the agent as the API returns it: the registry row plus the
// ordered chain of runners. The transport (kind/config) is not part of the
// agent any more — it belongs to the runner.
type agentResponse struct {
	agent.Agent
	Runners []runner.Ref `json:"runners"`
}

// agentWithChain reads an agent's chain and returns the wire shape.
func (s *Server) agentWithChain(ctx context.Context, sc tenancy.Scope, a agent.Agent) (agentResponse, error) {
	chain, err := s.runners.Chain(ctx, sc, a.ID)
	if err != nil {
		return agentResponse{}, err
	}
	refs := make([]runner.Ref, 0, len(chain))
	for _, r := range chain {
		refs = append(refs, runner.Ref{ID: r.ID, Name: r.Name})
	}
	return agentResponse{Agent: a, Runners: refs}, nil
}

// validateRunnerChain checks that every runner id in a chain exists in this
// tenant. It returns a user-facing message when one does not (the request is
// invalid) and an error when the registry could not be read.
func (s *Server) validateRunnerChain(ctx context.Context, sc tenancy.Scope, ids []string) (string, error) {
	for _, id := range ids {
		if _, err := s.runners.Get(ctx, sc, id); err != nil {
			if errors.Is(err, runner.ErrNotFound) {
				return fmt.Sprintf("Runner %s does not exist in this tenant.", id), nil
			}
			return "", err
		}
	}
	return "", nil
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	list, err := s.agents.ListAgents(r.Context(), sc)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	out := make([]agentResponse, 0, len(list))
	for _, a := range list {
		wire, err := s.agentWithChain(r.Context(), sc, a)
		if err != nil {
			s.writeAgentError(w, err)
			return
		}
		out = append(out, wire)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	a, err := s.agents.GetAgent(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	out, err := s.agentWithChain(r.Context(), sc, a)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": out})
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req agentCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	soul := ""
	if req.SoulMD != nil {
		soul = *req.SoulMD
	}
	role := agent.Role(req.Role)
	limit := req.ConcurrencyLimit
	if limit == 0 {
		limit = 2
	}
	if err := validateAgent(Validation{
		Name: req.Name, Role: role,
		Capabilities: req.Capabilities, SkillsPolicy: req.SkillsPolicy,
		ConcurrencyLimit: limit, SoulMD: soul,
	}); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_config", err.Error())
		return
	}
	// The chain is checked before anything is written, so a runner id that
	// does not exist never leaves a half-configured agent behind.
	if msg, err := s.validateRunnerChain(r.Context(), sc, req.Runners); err != nil {
		s.log.Error("agent handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	} else if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", msg)
		return
	}

	in := agent.Agent{
		Name:             req.Name,
		Role:             role,
		Capabilities:     normalizeJSONValue(req.Capabilities, "{}"),
		ConcurrencyLimit: limit,
		Enabled:          enabled,
		SoulMD:           soul,
		CredentialRef:    req.CredentialRef,
		SkillsPolicy:     normalizeJSONValue(req.SkillsPolicy, "{}"),
		CreatedBy:        &sc.UserID,
	}
	a, err := s.agents.CreateAgent(r.Context(), sc, in)
	if err != nil {
		if errors.Is(err, agent.ErrConflict) {
			writeError(w, http.StatusConflict, "agent_name_taken", "An agent with that name already exists in this tenant.")
			return
		}
		s.writeAgentError(w, err)
		return
	}
	if len(req.Runners) > 0 {
		if err := s.runners.SetChain(r.Context(), sc, a.ID, req.Runners); err != nil {
			s.writeRunnerError(w, err)
			return
		}
	}
	out, err := s.agentWithChain(r.Context(), sc, a)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"agent": out})
}

func (s *Server) handlePatchAgent(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req agentPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	cur, err := s.agents.GetAgent(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}

	patch := agent.AgentPatch{
		Name:             req.Name,
		ConcurrencyLimit: req.ConcurrencyLimit,
		Enabled:          req.Enabled,
		SoulMD:           req.SoulMD,
		CredentialRef:    req.CredentialRef,
	}
	if req.Role != nil {
		role := agent.Role(*req.Role)
		patch.Role = &role
	}
	if req.Capabilities != nil {
		patch.Capabilities = &req.Capabilities
	}
	if req.SkillsPolicy != nil {
		patch.SkillsPolicy = &req.SkillsPolicy
	}

	// Validate the effective row, not just the patch.
	eff := Validation{Name: cur.Name, Role: cur.Role,
		Capabilities: cur.Capabilities, SkillsPolicy: cur.SkillsPolicy,
		ConcurrencyLimit: cur.ConcurrencyLimit, SoulMD: cur.SoulMD}
	if req.Name != nil {
		eff.Name = *req.Name
	}
	if req.Role != nil {
		eff.Role = agent.Role(*req.Role)
	}
	if req.Capabilities != nil {
		eff.Capabilities = req.Capabilities
	}
	if req.SkillsPolicy != nil {
		eff.SkillsPolicy = req.SkillsPolicy
	}
	if req.ConcurrencyLimit != nil {
		eff.ConcurrencyLimit = *req.ConcurrencyLimit
	}
	if req.SoulMD != nil {
		eff.SoulMD = *req.SoulMD
	}
	if err := validateAgent(eff); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_config", err.Error())
		return
	}

	// An omitted chain leaves the agent's runners alone; an empty chain clears
	// them; a chain of ids replaces them, in order.
	if req.Runners != nil {
		if msg, err := s.validateRunnerChain(r.Context(), sc, *req.Runners); err != nil {
			s.log.Error("agent handler", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			return
		} else if msg != "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_request", msg)
			return
		}
	}

	a, err := s.agents.UpdateAgent(r.Context(), sc, cur.ID, patch, sc.UserID)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	if req.Runners != nil {
		if err := s.runners.SetChain(r.Context(), sc, a.ID, *req.Runners); err != nil {
			s.writeRunnerError(w, err)
			return
		}
	}
	out, err := s.agentWithChain(r.Context(), sc, a)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": out})
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := s.agents.DeleteAgent(r.Context(), sc, r.PathValue("id")); err != nil {
		s.writeAgentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- validation ------------------------------------------------------------

var (
	agentNameRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	maxSoulBytes = 200 << 10
)

// Validation is the effective agent shape the API checks before writing.
//
// The transport is not part of it any more: a runner's config is validated
// against the runner's kind, in internal/runner, where the transport lives.
type Validation struct {
	Name             string
	Role             agent.Role
	Capabilities     json.RawMessage
	SkillsPolicy     json.RawMessage
	ConcurrencyLimit int
	SoulMD           string
}

func validateAgent(v Validation) error {
	if !agentNameRE.MatchString(v.Name) {
		return errors.New("name must be lowercase letters, digits, '-' or '_' (max 64, starting alphanumeric)")
	}
	if !v.Role.Valid() {
		return fmt.Errorf("role must be one of pm, dev, reviewer, qa, researcher")
	}
	if v.ConcurrencyLimit < 1 || v.ConcurrencyLimit > 16 {
		return errors.New("concurrency_limit must be between 1 and 16")
	}
	if len(v.SoulMD) > maxSoulBytes {
		return fmt.Errorf("soul_md is limited to %d bytes", maxSoulBytes)
	}
	if len(v.Capabilities) > 0 && !isJSONObject(v.Capabilities) {
		return errors.New("capabilities must be a JSON object")
	}
	if len(v.SkillsPolicy) > 0 {
		var sp struct {
			Auto      bool `json:"auto"`
			MaxAttach int  `json:"max_attach"`
		}
		if err := strictUnmarshal(v.SkillsPolicy, &sp); err != nil {
			return fmt.Errorf("skills_policy: %v", err)
		}
		if sp.MaxAttach < 0 || sp.MaxAttach > 10 {
			return errors.New("skills_policy.max_attach must be between 0 and 10")
		}
	}
	return nil
}

func strictUnmarshal(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func isJSONObject(raw []byte) bool {
	var m map[string]any
	return json.Unmarshal(raw, &m) == nil && m != nil
}

func normalizeJSONObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}

func normalizeJSONValue(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(fallback)
	}
	return raw
}

// --- threads ---------------------------------------------------------------

type postMessageRequest struct {
	BodyMD string `json:"body_md"`
}

func (s *Server) handleItemThread(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	th, err := s.agents.EnsureItemThread(r.Context(), sc, r.PathValue("id"), sc.UserID)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": th})
}

func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	th, err := s.agents.GetThread(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": th})
}

func (s *Server) handleThreadMessages(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	th, err := s.agents.GetThread(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	msgs, err := s.agents.ListMessages(r.Context(), sc, th.ID, queryInt64(r, "after_seq", 0), queryInt(r, "limit", 100))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) handlePostThreadMessage(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req postMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	body := strings.TrimSpace(req.BodyMD)
	if body == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Write a message first.")
		return
	}
	th, err := s.agents.GetThread(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}

	// Resolve every mention before writing, so an unknown name fails the whole
	// request without leaving a message nobody answered.
	names := agent.ParseMentions(body)
	mentioned := make([]agent.Agent, 0, len(names))
	for _, name := range names {
		a, err := s.agents.GetAgentByName(r.Context(), sc, name)
		if errors.Is(err, agent.ErrNotFound) {
			writeError(w, http.StatusUnprocessableEntity, "unknown_agent",
				fmt.Sprintf("No agent named @%s exists in this tenant.", name))
			return
		}
		if err != nil {
			s.writeAgentError(w, err)
			return
		}
		if !a.Enabled {
			writeError(w, http.StatusUnprocessableEntity, "agent_disabled",
				fmt.Sprintf("@%s is disabled.", name))
			return
		}
		mentioned = append(mentioned, a)
	}

	msg, err := s.agents.AddMessage(r.Context(), sc, agent.NewMessage{
		ThreadID:     th.ID,
		AuthorKind:   agent.AuthorHuman,
		AuthorUserID: sc.UserID,
		BodyMD:       body,
	})
	if err != nil {
		s.writeAgentError(w, err)
		return
	}

	jobs := make([]agent.Job, 0, len(mentioned))
	for _, a := range mentioned {
		job, err := s.enqueueChatJob(r.Context(), sc, th, &msg, a)
		if err != nil {
			s.writeAgentError(w, err)
			return
		}
		jobs = append(jobs, job)
	}
	writeJSON(w, http.StatusCreated, map[string]any{"message": msg, "jobs": jobs})
}

type runAgentRequest struct {
	AgentID      string `json:"agent_id"`
	Instructions string `json:"instructions"`
}

func (s *Server) handleRunThreadAgent(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req runAgentRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	th, err := s.agents.GetThread(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	a, err := s.agents.GetAgent(r.Context(), sc, req.AgentID)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	if !a.Enabled {
		writeError(w, http.StatusUnprocessableEntity, "agent_disabled", "That agent is disabled.")
		return
	}

	var msg *agent.Message
	if text := strings.TrimSpace(req.Instructions); text != "" {
		m, err := s.agents.AddMessage(r.Context(), sc, agent.NewMessage{
			ThreadID:     th.ID,
			AuthorKind:   agent.AuthorHuman,
			AuthorUserID: sc.UserID,
			BodyMD:       text,
		})
		if err != nil {
			s.writeAgentError(w, err)
			return
		}
		msg = &m
	}

	job, err := s.enqueueChatJob(r.Context(), sc, th, msg, a)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job, "message": msg})
}

func (s *Server) handleThreadJobs(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	th, err := s.agents.GetThread(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	jobs, err := s.agents.ListJobsByThread(r.Context(), sc, th.ID, queryInt(r, "limit", 20))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

// enqueueChatJob assembles the prompt (soul → context → history → instruction)
// and queues the job. history is read after the triggering message is inserted,
// so the latest human turn is included.
func (s *Server) enqueueChatJob(ctx context.Context, sc tenancy.Scope, th agent.Thread, trigger *agent.Message, a agent.Agent) (agent.Job, error) {
	pc := agent.PromptContext{AgentName: a.Name, AgentRole: a.Role, ThreadTitle: th.Title}
	if th.ItemID != nil {
		if it, err := s.work.GetItem(ctx, sc, *th.ItemID); err == nil {
			pc.ItemID = it.ID
			pc.ItemTitle = it.Title
			pc.ItemStatus = it.Status
			pc.ItemKind = it.Kind
			pc.ItemDescription = it.DescriptionMD
			if it.Priority != nil {
				pc.ItemPriority = *it.Priority
			}
		}
	}
	history, err := s.agents.ListMessages(ctx, sc, th.ID, 0, 100)
	if err != nil {
		return agent.Job{}, err
	}
	prompt := agent.BuildChatPrompt(a.SoulMD, pc, history)

	in := agent.NewJob{
		AgentID:   a.ID,
		ThreadID:  th.ID,
		Prompt:    prompt,
		CreatedBy: sc.UserID,
	}
	if th.ItemID != nil {
		in.ItemID = *th.ItemID
	}
	if trigger != nil {
		in.MessageID = trigger.ID
	}
	ctxJSON, _ := json.Marshal(map[string]any{
		"context_kind": th.ContextKind,
		"item_id":      itemIDOrEmpty(th),
		"thread_id":    th.ID,
		"agent":        a.Name,
		"assembled_at": time.Now().UTC().Format(time.RFC3339),
	})
	in.Context = ctxJSON
	return s.agents.CreateJob(ctx, sc, in)
}

// --- jobs ------------------------------------------------------------------

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	job, err := s.agents.GetJob(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": job})
}

func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	job, err := s.agents.GetJob(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	logs, err := s.agents.ListLogs(r.Context(), sc, job.ID, queryInt64(r, "after_seq", 0), queryInt(r, "limit", 500))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "job": job})
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	job, err := s.agents.GetJob(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	if job.State.Terminal() {
		writeError(w, http.StatusConflict, "job_terminal", "That job already finished.")
		return
	}
	updated, err := s.agents.CancelJob(r.Context(), sc, job.ID)
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	if s.agentWorker != nil {
		s.agentWorker.Cancel(job.ID)
	}
	s.agentHub.Publish(agent.StreamEvent{Type: "state", Job: &updated})
	writeJSON(w, http.StatusAccepted, map[string]any{"job": updated})
}

// handleJobEvents streams a job's log as Server-Sent Events: replay from
// after_seq, then live from the hub, with a periodic state check so a job that
// finished between processes still closes the stream.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	job, err := s.agents.GetJob(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeAgentError(w, err)
		return
	}
	after := queryInt64(r, "after_seq", 0)

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // SSE connections outlive WriteTimeout
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}

	live, unsubscribe := s.agentHub.Subscribe(job.ID)
	defer unsubscribe()

	last := after
	for {
		logs, err := s.agents.ListLogs(r.Context(), sc, job.ID, last, 500)
		if err != nil {
			s.writeSSE(w, "error", map[string]string{"message": "Could not load the job log."})
			flush()
			return
		}
		for _, e := range logs {
			s.writeSSE(w, "log", e)
			last = e.Seq
		}
		if len(logs) < 500 {
			break
		}
	}
	if job.State.Terminal() {
		s.writeSSE(w, "state", map[string]any{"job": job})
		s.writeSSE(w, "done", struct{}{})
		flush()
		return
	}
	s.writeSSE(w, "state", map[string]any{"job": job})
	flush()

	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	poll := time.NewTicker(5 * time.Second)
	defer poll.Stop()
	lastState := job.State

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-live:
			if !ok {
				return // slow subscriber; the client reconnects and replays
			}
			if ev.Log != nil {
				if ev.Log.Seq <= last {
					continue
				}
				s.writeSSE(w, "log", *ev.Log)
				last = ev.Log.Seq
				flush()
			}
			if ev.Job != nil {
				lastState = ev.Job.State
				s.writeSSE(w, "state", map[string]any{"job": *ev.Job})
				flush()
				if ev.Job.State.Terminal() {
					s.writeSSE(w, "done", struct{}{})
					flush()
					return
				}
			}
		case <-keepAlive.C:
			fmt.Fprint(w, ": ping\n\n")
			flush()
		case <-poll.C:
			cur, err := s.agents.GetJob(context.Background(), sc, job.ID)
			if err != nil {
				continue
			}
			if cur.State != lastState {
				lastState = cur.State
				s.writeSSE(w, "state", map[string]any{"job": cur})
				flush()
			}
			if cur.State.Terminal() {
				// Drain anything the hub missed, then close.
				for {
					logs, err := s.agents.ListLogs(context.Background(), sc, job.ID, last, 500)
					if err != nil || len(logs) == 0 {
						break
					}
					for _, e := range logs {
						s.writeSSE(w, "log", e)
						last = e.Seq
					}
				}
				s.writeSSE(w, "done", struct{}{})
				flush()
				return
			}
		}
	}
}

func (s *Server) writeSSE(w http.ResponseWriter, event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

func itemIDOrEmpty(th agent.Thread) string {
	if th.ItemID == nil {
		return ""
	}
	return *th.ItemID
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func queryInt64(r *http.Request, key string, fallback int64) int64 {
	v := r.URL.Query().Get(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

// --- errors ----------------------------------------------------------------

func (s *Server) writeAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agent.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That agent resource does not exist in this tenant.")
	case errors.Is(err, agent.ErrDisabled):
		writeError(w, http.StatusUnprocessableEntity, "agent_disabled", "That agent is disabled.")
	case errors.Is(err, agent.ErrQueueFull):
		writeError(w, http.StatusUnprocessableEntity, "queue_full", "Too many agent jobs are already queued in this tenant.")
	case errors.Is(err, agent.ErrNotRunning):
		writeError(w, http.StatusConflict, "job_terminal", "That job already finished.")
	case errors.Is(err, agent.ErrConflict):
		writeError(w, http.StatusConflict, "agent_in_use",
			"That agent cannot be changed: it has job history. Disable it instead.")
	case errors.Is(err, agent.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", err.Error())
	case errors.Is(err, tenancy.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
	default:
		s.log.Error("agent handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
