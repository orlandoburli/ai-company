package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/agent"
)

// apiServer is a minimal recording server.
type apiServer struct {
	*httptest.Server

	mu     sync.Mutex
	path   string
	auth   string
	ver    string
	xKey   string
	body   map[string]any
	calls  int
	status int
	resp   string
}

func newAPIServer(t *testing.T, status int, resp string) *apiServer {
	t.Helper()
	s := &apiServer{status: status, resp: resp}
	s.Server = httptest.NewServer(s.handler())
	t.Cleanup(s.Server.Close)
	return s
}

func newAPIServerFn(t *testing.T, fn func(w http.ResponseWriter, r *http.Request, calls int)) *apiServer {
	t.Helper()
	s := &apiServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls++
		n := s.calls
		s.path = r.URL.Path
		s.auth = r.Header.Get("Authorization")
		s.ver = r.Header.Get("anthropic-version")
		s.xKey = r.Header.Get("x-api-key")
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		s.body = m
		s.mu.Unlock()
		fn(w, r, n)
	}))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *apiServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls++
		s.path = r.URL.Path
		s.auth = r.Header.Get("Authorization")
		s.ver = r.Header.Get("anthropic-version")
		s.xKey = r.Header.Get("x-api-key")
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		s.body = m
		w.WriteHeader(s.status)
		s.mu.Unlock()
		io.WriteString(w, s.resp) //nolint:errcheck // test server
	}
}

func (s *apiServer) round() (path, auth, ver, xKey string, body map[string]any, calls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path, s.auth, s.ver, s.xKey, s.body, s.calls
}

func apiJob(id string, cfg agent.APIConfig) agent.Job {
	raw, err := json.Marshal(cfg)
	if err != nil {
		panic(err)
	}
	return agent.Job{ID: id, Config: raw, Prompt: "hello model"}
}

func TestAPIOpenAI(t *testing.T) {
	srv := newAPIServer(t, http.StatusOK, `{"choices":[{"message":{"content":"hi there"}}],
		"usage":{"prompt_tokens":7,"completion_tokens":9}}`)
	t.Setenv("AIC_TEST_OPENAI_KEY", "sk-testkey123456")

	var c collector
	res, err := NewAPI().Run(context.Background(), apiJob("a1", agent.APIConfig{
		Provider:  "openai",
		Model:     "gpt-test",
		BaseURL:   srv.URL + "/v1",
		KeyEnv:    "AIC_TEST_OPENAI_KEY",
		MaxTokens: 128,
		Temp:      0.5,
	}), c.sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Output != "hi there" {
		t.Errorf("Output = %q", res.Output)
	}
	if res.TokensIn != 7 || res.TokensOut != 9 {
		t.Errorf("tokens = %d/%d, want 7/9", res.TokensIn, res.TokensOut)
	}
	if res.CostUSD != nil {
		t.Errorf("CostUSD = %v, want nil", res.CostUSD)
	}
	path, auth, _, _, body, calls := srv.round()
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
	if path != "/v1/chat/completions" {
		t.Errorf("path = %q", path)
	}
	if auth != "Bearer sk-testkey123456" {
		t.Errorf("auth = %q", auth)
	}
	if body["model"] != "gpt-test" {
		t.Errorf("model = %v", body["model"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["content"] != "hello model" {
		t.Errorf("messages = %v", body["messages"])
	}
	if body["max_tokens"] != float64(128) || body["temperature"] != 0.5 {
		t.Errorf("max_tokens/temperature = %v/%v", body["max_tokens"], body["temperature"])
	}
	if _, ok := c.find("output_chunk"); !ok {
		t.Errorf("missing output_chunk event")
	}
	if _, ok := c.find("usage"); !ok {
		t.Errorf("missing usage event")
	}
}

func TestAPIOpenAIOmitZeroes(t *testing.T) {
	srv := newAPIServer(t, http.StatusOK, `{"choices":[{"message":{"content":"x"}}],"usage":{}}`)
	t.Setenv("AIC_TEST_OPENAI_KEY", "sk-testkey123456")
	_, err := NewAPI().Run(context.Background(), apiJob("a2", agent.APIConfig{
		Provider: "openai", Model: "m", BaseURL: srv.URL + "/v1", KeyEnv: "AIC_TEST_OPENAI_KEY",
	}), (&collector{}).sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, _, _, _, body, _ := srv.round()
	if _, ok := body["max_tokens"]; ok {
		t.Errorf("max_tokens should be omitted when 0")
	}
	if _, ok := body["temperature"]; ok {
		t.Errorf("temperature should be omitted when 0")
	}
}

func TestAPIAnthropic(t *testing.T) {
	srv := newAPIServer(t, http.StatusOK, `{"content":[{"type":"text","text":"part one"},
		{"type":"tool_use","id":"t"},{"type":"text","text":"part two"}],
		"usage":{"input_tokens":5,"output_tokens":6}}`)
	t.Setenv("AIC_TEST_ANTHROPIC_KEY", "sk-ant-testkey456")

	var c collector
	res, err := NewAPI().Run(context.Background(), apiJob("a3", agent.APIConfig{
		Provider: "anthropic",
		Model:    "claude-test",
		BaseURL:  srv.URL + "/v1",
		KeyEnv:   "AIC_TEST_ANTHROPIC_KEY",
	}), c.sink())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Output != "part one\npart two" {
		t.Errorf("Output = %q, want joined text blocks", res.Output)
	}
	if res.TokensIn != 5 || res.TokensOut != 6 {
		t.Errorf("tokens = %d/%d", res.TokensIn, res.TokensOut)
	}
	path, _, ver, xKey, body, _ := srv.round()
	if path != "/v1/messages" {
		t.Errorf("path = %q", path)
	}
	if xKey != "sk-ant-testkey456" {
		t.Errorf("x-api-key = %q", xKey)
	}
	if ver != "2023-06-01" {
		t.Errorf("anthropic-version = %q", ver)
	}
	if body["max_tokens"] != float64(1024) {
		t.Errorf("max_tokens = %v, want default 1024", body["max_tokens"])
	}
	if _, ok := c.find("usage"); !ok {
		t.Errorf("missing usage event")
	}
}

func TestAPIFailureClassification(t *testing.T) {
	cases := []struct {
		status int
		kind   string
	}{
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusUnauthorized, "auth"},
		{http.StatusForbidden, "auth"},
		{http.StatusInternalServerError, "transient"},
		{http.StatusServiceUnavailable, "transient"},
		{http.StatusBadRequest, "capability"},
		{http.StatusNotFound, "capability"},
		{http.StatusUnprocessableEntity, "capability"},
		{http.StatusConflict, "permanent"},
	}
	for _, tc := range cases {
		srv := newAPIServer(t, tc.status, `{"error":{"message":"nope"}}`)
		t.Setenv("AIC_TEST_KEY", "sk-x")
		_, err := NewAPI().Run(context.Background(), apiJob("a4", agent.APIConfig{
			Provider: "openai", Model: "m", BaseURL: srv.URL + "/v1", KeyEnv: "AIC_TEST_KEY",
		}), (&collector{}).sink())
		if err == nil {
			t.Fatalf("status %d: want error", tc.status)
		}
		if got := agent.FailureKind(err); got != tc.kind {
			t.Errorf("status %d: agent.FailureKind = %q, want %q", tc.status, got, tc.kind)
		}
		assertClassified(t, err)
		if !strings.Contains(err.Error(), "http") {
			t.Errorf("status %d: error %q lacks status text", tc.status, err)
		}
	}
}

func TestAPIMissingKeyEnv(t *testing.T) {
	_, err := NewAPI().Run(context.Background(), apiJob("a5", agent.APIConfig{
		Provider: "openai", Model: "m", KeyEnv: "AIC_DEFINITELY_UNSET_KEY",
	}), (&collector{}).sink())
	if err == nil {
		t.Fatal("missing key must error")
	}
	// The error names the env var; it can never contain a secret value.
	if !strings.Contains(err.Error(), "AIC_DEFINITELY_UNSET_KEY") {
		t.Errorf("error %q must name the env var", err)
	}
	assertClassified(t, err)
	if agent.FailureKind(err) != agent.FailurePermanent {
		t.Errorf("missing key → agent.FailureKind = %q, want permanent", agent.FailureKind(err))
	}
}

func TestAPIInvalidConfig(t *testing.T) {
	a := NewAPI()
	if _, err := a.Run(context.Background(), agent.Job{ID: "a6", Config: nil},
		(&collector{}).sink()); err == nil {
		t.Error("nil config should error")
	}
	if _, err := a.Run(context.Background(), apiJob("a7", agent.APIConfig{Model: "m", KeyEnv: "X"}),
		(&collector{}).sink()); err == nil {
		t.Error("bad provider should error")
	}
	if _, err := a.Run(context.Background(), apiJob("a8", agent.APIConfig{Provider: "openai", KeyEnv: "X"}),
		(&collector{}).sink()); err == nil {
		t.Error("missing model should error")
	}
}

func TestAPICancelRegistry(t *testing.T) {
	a := NewAPI()
	srv := newAPIServerFn(t, func(w http.ResponseWriter, r *http.Request, n int) {
		// Slow enough for Cancel to land; client respects call ctx.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"choices":[{"message":{"content":"late"}}]}`) //nolint:errcheck
		}
	})
	t.Setenv("AIC_TEST_KEY", "sk-x")

	ctx := context.Background()
	type outcome struct {
		res *agent.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		r, e := a.Run(ctx, apiJob("a9", agent.APIConfig{
			Provider: "openai", Model: "m", BaseURL: srv.URL + "/v1", KeyEnv: "AIC_TEST_KEY",
		}), (&collector{}).sink())
		done <- outcome{r, e}
	}()
	time.Sleep(200 * time.Millisecond)
	if err := a.Cancel("a9"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case o := <-done:
		if o.err == nil {
			t.Fatalf("want error, got %+v", o.res)
		}
		if !strings.Contains(o.err.Error(), "canceled") {
			t.Errorf("error %q should be a cancellation", o.err)
		}
		if agent.FailureKind(o.err) != agent.FailureTransient {
			t.Errorf("agent.FailureKind = %q, want transient", agent.FailureKind(o.err))
		}
		assertClassified(t, o.err)
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after Cancel")
	}
	// After completion, cancelling again is a no-op (idempotent).
	if err := a.Cancel("a9"); err != nil {
		t.Errorf("Cancel after finish = %v, want nil", err)
	}
	if err := a.Cancel("unknown-job"); err != nil {
		t.Errorf("Cancel(unknown) = %v, want nil", err)
	}
}

func TestFailureKindNonAPIErrors(t *testing.T) {
	if got := agent.FailureKind(context.Canceled); got != agent.FailureTransient {
		t.Errorf("ctx.Canceled → %q, want transient", got)
	}
	if got := agent.FailureKind(context.DeadlineExceeded); got != agent.FailureTransient {
		t.Errorf("DeadlineExceeded → %q, want transient", got)
	}
	if got := FailureKind(io.ErrUnexpectedEOF); got != agent.FailureTransient {
		t.Errorf("EOF → %q, want transient", got)
	}
	if got := FailureKind(errors.New("bogus")); got != agent.FailurePermanent {
		t.Errorf("plain error → %q, want permanent", got)
	}
	if agent.FailureKind(nil) != "" {
		t.Errorf("nil error should classify as empty")
	}
}
