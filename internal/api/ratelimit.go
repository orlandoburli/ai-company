package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Rate limiting for the unauthenticated and abuse-prone endpoints.
//
// This is deliberately in memory: the MVP runs exactly one server process
// (docs/mvp-design.md §10), so counters living in that process are
// authoritative. Two consequences follow, both documented in
// docs/implementation-notes.md: the limits are per-process — run more than one
// replica and each gets its own budget — and they reset whenever the process
// restarts.
//
// Keys for the unauthenticated buckets come from the request itself, so an
// attacker effectively chooses them; the counter map is therefore pruned
// instead of being allowed to grow with the number of distinct keys.

// Buckets, as used as the keys of the WithRateLimits map.
const (
	// BucketLogin guards POST /api/auth/login, keyed on client IP +
	// lowercased email.
	BucketLogin = "login"
	// BucketAcceptInvite guards POST /api/auth/accept-invite, keyed on
	// client IP.
	BucketAcceptInvite = "accept-invite"
	// BucketCreateInvite guards POST /api/invites, keyed on the acting
	// user's id.
	BucketCreateInvite = "create-invite"
)

// Defaults per bucket. Failure-biased: the login counter is charged on failed
// verification and cleared on success, so an earlier typo storm never locks the
// real user out.
const (
	defaultLoginMax    = 10
	defaultLoginWindow = 5 * time.Minute

	defaultAcceptInviteMax    = 20
	defaultAcceptInviteWindow = time.Hour

	defaultCreateInviteMax    = 30
	defaultCreateInviteWindow = time.Hour

	// rateKeyCap bounds the live counters. A lazy sweep keeps the map bounded
	// without a timer goroutine — nothing here spawns anything, so the test
	// suite cannot leak goroutines — and the worst case is rateKeyCap small
	// entries, well under a megabyte.
	rateKeyCap = 8192
)

// RateLimit overrides one limiter bucket in WithRateLimits. Both fields zero
// mean "keep the defaults"; a single zero field keeps that one default, so a
// caller can tune a single knob — tests use small windows like
// map[string]RateLimit{BucketLogin: {Max: 2, Window: time.Minute}}.
type RateLimit struct {
	Max    int           // allowed attempts per window
	Window time.Duration // window length
}

// defaultRateLimits is rebuilt per server so an option's merge cannot leak into
// the next server in the same process.
func defaultRateLimits() map[string]rateLimit {
	return map[string]rateLimit{
		BucketLogin:        {max: defaultLoginMax, window: defaultLoginWindow},
		BucketAcceptInvite: {max: defaultAcceptInviteMax, window: defaultAcceptInviteWindow},
		BucketCreateInvite: {max: defaultCreateInviteMax, window: defaultCreateInviteWindow},
	}
}

type rateLimit struct {
	max    int
	window time.Duration
}

// rateLimiter is a fixed-window counter: a charged attempt is allowed while the
// count within the window has not passed the bucket's max. It is a mutexed map
// with no background goroutine; pruning happens lazily on the request path.
type rateLimiter struct {
	defs  map[string]rateLimit
	count map[string]rateState

	mu sync.Mutex
}

type rateState struct {
	windowStart time.Time
	attempts    int
}

func newRateLimiter(defs map[string]rateLimit) *rateLimiter {
	return &rateLimiter{defs: defs, count: make(map[string]rateState)}
}

// attempt charges one attempt to the bucket/key and reports whether it stays
// within the budget. retryAfter is what the client should be told to wait
// (always at least one second, because it surfaces as an integer Retry-After).
// The zero-value lookalike case — an undeclared bucket — is unbounded rather
// than unusable.
func (l *rateLimiter) attempt(bucket, key string, now time.Time) (retryAfter time.Duration, ok bool) {
	def, defined := l.defs[bucket]
	if !defined || def.max <= 0 || def.window <= 0 {
		return 0, true
	}
	id := bucket + "\x00" + key

	l.mu.Lock()
	defer l.mu.Unlock()

	st, seen := l.count[id]
	if !seen || now.Sub(st.windowStart) >= def.window {
		if !seen && len(l.count) >= rateKeyCap {
			l.pruneLocked(now)
			// The sweep can come back empty when distinct keys rotate within a
			// single window. Dropping an arbitrary (Go: unspecified order)
			// entry trades a stranger's window restart for bounded memory;
			// that is the right trade under an adversarial key set.
			for len(l.count) >= rateKeyCap {
				for k := range l.count {
					delete(l.count, k)
					break
				}
			}
		}
		st = rateState{windowStart: now}
	}
	st.attempts++
	l.count[id] = st

	if st.attempts <= def.max {
		return 0, true
	}
	retryAfter = st.windowStart.Add(def.window).Sub(now)
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return retryAfter, false
}

// clear drops the counter for a key. A successful login calls this so the next
// burst of typos is judged on its own instead of inheriting the window count.
func (l *rateLimiter) clear(bucket, key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.count, bucket+"\x00"+key)
}

// pruneLocked discards counters whose window has elapsed. The bucket name rides
// inside the counter key, so each entry is judged against its own window.
func (l *rateLimiter) pruneLocked(now time.Time) {
	for k, st := range l.count {
		bucket, _, _ := strings.Cut(k, "\x00")
		def, defined := l.defs[bucket]
		if !defined || now.Sub(st.windowStart) >= def.window {
			delete(l.count, k)
		}
	}
}

// WithRateLimits merges overrides for the named buckets on top of the defaults.
// Zero fields keep the bucket's default.
func WithRateLimits(overrides map[string]RateLimit) Option {
	return func(s *Server) {
		for bucket, rl := range overrides {
			cur := s.limits.defs[bucket]
			if rl.Max != 0 {
				cur.max = rl.Max
			}
			if rl.Window != 0 {
				cur.window = rl.Window
			}
			s.limits.defs[bucket] = cur
		}
	}
}

// clientKey is what unauthenticated rate limiting keys on: the TCP peer address
// from r.RemoteAddr. X-Forwarded-For is deliberately ignored — without a
// trusted proxy the client writes that header itself, so honouring it would let
// an attacker rotate their apparent address freely. Parse the header at an
// edge that owns the trust decision, not here.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// rateAllow charges one attempt to the bucket. When the bucket is exhausted it
// writes the 429 with a Retry-After header and returns false.
func (s *Server) rateAllow(w http.ResponseWriter, bucket, key string) bool {
	retryAfter, ok := s.limits.attempt(bucket, key, time.Now())
	if ok {
		return true
	}
	s.log.Warn("rate limited", "bucket", bucket)
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter/time.Second)))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later.")
	return false
}

// rateClear drops the counter for a key after a success (see rateLimiter.clear).
func (s *Server) rateClear(bucket, key string) {
	s.limits.clear(bucket, key)
}
