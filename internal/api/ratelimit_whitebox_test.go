package api

import (
	"fmt"
	"testing"
	"time"
)

// White-box checks on the limiter itself: the pruning keeps the map bounded
// under attacker-chosen keys, and windows advance by clock values passed in, so
// no test sleeps.

func TestRateLimiterPrunesAtKeyCap(t *testing.T) {
	l := newRateLimiter(map[string]rateLimit{
		BucketLogin: {max: 1, window: time.Hour},
	})
	now := time.Now()

	// 3x the cap of distinct attacker keys: every first attempt is allowed,
	// and the map must never hold more than the cap.
	for i := 0; i < 3*rateKeyCap; i++ {
		now = now.Add(time.Millisecond)
		if _, ok := l.attempt(BucketLogin, fmt.Sprintf("10.0.0.%d", i), now); !ok {
			t.Fatalf("attempt %d denied", i)
		}
	}

	l.mu.Lock()
	n := len(l.count)
	l.mu.Unlock()
	if n > rateKeyCap {
		t.Errorf("counter map holds %d keys; cap is %d", n, rateKeyCap)
	}
}

func TestRateLimiterWindowAdvancesWithoutSleeping(t *testing.T) {
	l := newRateLimiter(map[string]rateLimit{
		BucketLogin: {max: 1, window: time.Minute},
	})
	base := time.Now()

	if _, ok := l.attempt(BucketLogin, "k", base); !ok {
		t.Fatal("first attempt denied")
	}
	if _, ok := l.attempt(BucketLogin, "k", base); ok {
		t.Fatal("second attempt within the window allowed")
	}

	// A later clock value starts a fresh window; pruning triggered by the cap
	// would also have removed the expired entry by then.
	if _, ok := l.attempt(BucketLogin, "k", base.Add(time.Minute)); !ok {
		t.Fatal("next window never opened")
	}

	// Success clears the counter so a later first attempt starts at one.
	l.clear(BucketLogin, "k")
	l.mu.Lock()
	_, present := l.count[BucketLogin+"\x00"+"k"]
	l.mu.Unlock()
	if present {
		t.Error("cleared counter is still present")
	}
}
