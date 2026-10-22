package api_test

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/api"
)

// Rate limiting. The buckets are injectable, so tests use small windows and
// maxima instead of sleeping out the five-minute and hourly defaults.

func TestLoginRateLimitedButNotLockedOutOnSuccess(t *testing.T) {
	h := newHarnessWith(t, api.WithRateLimits(map[string]api.RateLimit{
		api.BucketLogin: {Max: 2, Window: time.Minute},
	}))
	if res := h.register("owner@example.com", goodPassword, "Acme", "acme"); res.status != http.StatusCreated {
		t.Fatalf("register: %d %s", res.status, res.body)
	}

	login := func(password string) response {
		h.t.Helper()
		return h.do("POST", "/api/auth/login", map[string]any{
			"email": "owner@example.com", "password": password,
		}, "")
	}

	// A burst of wrong passwords exhausts the bucket: the third failure is a
	// 429 with the common envelope and a Retry-After in whole seconds.
	if res := login("wrong-1"); res.status != http.StatusUnauthorized {
		t.Fatalf("first failure: %d %s", res.status, res.body)
	}
	if res := login("wrong-2"); res.status != http.StatusUnauthorized {
		t.Fatalf("second failure: %d %s", res.status, res.body)
	}
	third := login("wrong-3")
	if third.status != http.StatusTooManyRequests {
		t.Fatalf("third failure: %d %s; want 429", third.status, third.body)
	}
	if code := third.json(t)["error"].(map[string]any)["code"]; code != "rate_limited" {
		t.Fatalf("429 code = %v; want rate_limited", code)
	}
	retry, err := strconv.Atoi(third.header.Get("Retry-After"))
	if err != nil || retry <= 0 {
		t.Fatalf("Retry-After = %q; want a positive integer", third.header.Get("Retry-After"))
	}

	// The correct password is never refused by the limiter: it answers before
	// any charging, and it clears the counter. So an earlier typo storm cannot
	// lock the real user out...
	if res := login(goodPassword); res.status != http.StatusOK {
		t.Fatalf("good login after burst: %d %s; want 200", res.status, res.body)
	}

	// ...and the window is fresh again, so the next burst has to rebuild.
	if res := login("wrong-a"); res.status != http.StatusUnauthorized {
		t.Fatalf("post-reset failure 1: %d %s", res.status, res.body)
	}
	if res := login("wrong-b"); res.status != http.StatusUnauthorized {
		t.Fatalf("post-reset failure 2: %d %s", res.status, res.body)
	}
	if res := login("wrong-c"); res.status != http.StatusTooManyRequests {
		t.Fatalf("post-reset failure 3: %d %s; want 429", res.status, res.body)
	}
}

func TestAcceptInviteRateLimitedPerIP(t *testing.T) {
	h := newHarnessWith(t, api.WithRateLimits(map[string]api.RateLimit{
		api.BucketAcceptInvite: {Max: 2, Window: time.Minute},
	}))
	o := newOwnerHarnessFor(t, h)

	res := o.do("POST", "/api/invites", map[string]any{
		"email": "new@example.com", "role": "member",
	}, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("create invite: %d %s", res.status, res.body)
	}
	token := res.json(t)["token"].(string)

	accept := func() response {
		h.t.Helper()
		return h.do("POST", "/api/auth/accept-invite", map[string]any{
			"token": token, "password": goodPassword, "name": "New Person",
		}, "")
	}

	// Attempt 1 consumes the token (201). Attempt 2 still reaches the handler
	// and fails with the usual 400. Attempt 3 is the limiter's, not the handler's.
	if res := accept(); res.status != http.StatusCreated {
		t.Fatalf("first accept: %d %s", res.status, res.body)
	}
	if res := accept(); res.status != http.StatusBadRequest {
		t.Fatalf("second accept: %d %s; want 400 (token burned)", res.status, res.body)
	}
	third := accept()
	if third.status != http.StatusTooManyRequests {
		t.Fatalf("third accept: %d %s; want 429", third.status, third.body)
	}
	if code := third.json(t)["error"].(map[string]any)["code"]; code != "rate_limited" {
		t.Fatalf("429 code = %v", code)
	}
	if third.header.Get("Retry-After") == "" {
		t.Error("429 missing Retry-After")
	}
}

func TestCreateInviteRateLimitedPerUser(t *testing.T) {
	h := newHarnessWith(t, api.WithRateLimits(map[string]api.RateLimit{
		api.BucketCreateInvite: {Max: 1, Window: time.Minute},
	}))
	o := newOwnerHarnessFor(t, h)

	res := o.do("POST", "/api/invites", map[string]any{"email": "a@example.com"}, o.ownerCookie)
	if res.status != http.StatusCreated {
		t.Fatalf("first invite: %d %s", res.status, res.body)
	}
	second := o.do("POST", "/api/invites", map[string]any{"email": "b@example.com"}, o.ownerCookie)
	if second.status != http.StatusTooManyRequests {
		t.Fatalf("second invite: %d %s; want 429", second.status, second.body)
	}
	if code := second.json(t)["error"].(map[string]any)["code"]; code != "rate_limited" {
		t.Fatalf("429 code = %v", code)
	}
	if second.header.Get("Retry-After") == "" {
		t.Error("429 missing Retry-After")
	}
}
