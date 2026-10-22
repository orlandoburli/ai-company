package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/auth"
)

func TestPasswordRoundTrip(t *testing.T) {
	const pw = "correct-horse-battery-staple"

	hash, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Errorf("hash is not a PHC argon2id string: %q", hash)
	}
	if strings.Contains(hash, pw) {
		t.Fatal("hash contains the plaintext password")
	}
	if err := auth.VerifyPassword(hash, pw); err != nil {
		t.Errorf("verify correct password: %v", err)
	}
	if err := auth.VerifyPassword(hash, pw+"x"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("verify wrong password = %v, want ErrInvalidCredentials", err)
	}
}

func TestHashesAreSaltedIndependently(t *testing.T) {
	const pw = "correct-horse-battery-staple"
	a, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("two hashes of the same password are identical; the salt is not random")
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, pw := range []string{"", "short", strings.Repeat("a", auth.MinPasswordLen-1)} {
		if _, err := auth.HashPassword(pw); !errors.Is(err, auth.ErrWeakPassword) {
			t.Errorf("HashPassword(len=%d) = %v, want ErrWeakPassword", len(pw), err)
		}
	}
	// The upper bound is a DoS guard, not a strength rule.
	if _, err := auth.HashPassword(strings.Repeat("a", auth.MaxPasswordLen+1)); !errors.Is(err, auth.ErrWeakPassword) {
		t.Error("oversized password accepted")
	}
	if _, err := auth.HashPassword(strings.Repeat("a", auth.MinPasswordLen)); err != nil {
		t.Errorf("minimum-length password rejected: %v", err)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, bad := range []string{
		"",
		"plaintext",
		"$argon2id$v=19$m=65536,t=3,p=2$onlythree",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=99$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=notanumber$c2FsdA$aGFzaA",
	} {
		if err := auth.VerifyPassword(bad, "anything"); err == nil {
			t.Errorf("malformed hash %q verified", bad)
		}
	}
}

func TestVerifyOrDummyDoesNotRevealMissingUser(t *testing.T) {
	// An empty hash means "no such user"; the result must still be the same
	// generic error, and it must still do the hashing work.
	if err := auth.VerifyOrDummy("", "some-password"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("= %v, want ErrInvalidCredentials", err)
	}
}

func TestSessionTokens(t *testing.T) {
	tok1, dig1, err := auth.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	tok2, dig2, _ := auth.NewSessionToken()

	if tok1 == tok2 || dig1 == dig2 {
		t.Fatal("session tokens are not unique")
	}
	if dig1 == tok1 {
		t.Fatal("stored digest equals the plaintext token")
	}
	if auth.HashToken(tok1) != dig1 {
		t.Error("HashToken does not reproduce the generated digest")
	}
	// 32 bytes of entropy, base64url without padding.
	if len(tok1) != 43 {
		t.Errorf("token length = %d, want 43", len(tok1))
	}
}
