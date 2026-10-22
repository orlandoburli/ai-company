// Package auth handles password hashing and opaque session tokens.
//
// Passwords are hashed with Argon2id and stored as PHC strings, so the cost
// parameters travel with the hash and can be raised later without a flag day:
// an old hash still verifies with the parameters it was created with.
//
// Session tokens are random, stored only as SHA-256 digests. A database dump
// therefore does not hand out live sessions; the plaintext token exists only in
// the client's cookie.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. 64 MiB / 3 passes / 2 lanes is a deliberately ordinary
// choice: enough to make offline cracking expensive, low enough that a login
// does not stall a small server.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16

	// Password length bounds. The upper bound is a denial-of-service guard: a
	// megabyte password would cost a megabyte of hashing work per request.
	MinPasswordLen = 12
	MaxPasswordLen = 1024
)

// ErrInvalidCredentials is returned for both an unknown user and a wrong
// password. Callers must not distinguish the two to the client.
var ErrInvalidCredentials = errors.New("auth: invalid credentials")

// ErrWeakPassword is returned when a password fails the length policy.
var ErrWeakPassword = fmt.Errorf("auth: password must be between %d and %d characters", MinPasswordLen, MaxPasswordLen)

// HashPassword returns a PHC-formatted Argon2id hash:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLen || len(password) > MaxPasswordLen {
		return "", ErrWeakPassword
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC-encoded hash. It
// returns nil only on a match. Comparison is constant time.
//
// The cost parameters are taken from the stored hash, not from this build's
// constants, which is what allows them to be raised without invalidating
// existing users.
func VerifyPassword(encoded, password string) error {
	params, salt, want, err := parsePHC(encoded)
	if err != nil {
		return err
	}
	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
}

func parsePHC(encoded string) (argonParams, []byte, []byte, error) {
	var p argonParams

	// $argon2id$v=19$m=65536,t=3,p=2$salt$hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errors.New("auth: not an argon2id PHC hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, fmt.Errorf("auth: parse version: %w", err)
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("auth: unsupported argon2 version %d", version)
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return p, nil, nil, fmt.Errorf("auth: parse cost: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, fmt.Errorf("auth: decode salt: %w", err)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, fmt.Errorf("auth: decode hash: %w", err)
	}
	if len(salt) == 0 || len(key) == 0 {
		return p, nil, nil, errors.New("auth: empty salt or hash")
	}
	return p, salt, key, nil
}

// dummyHash exists so login can spend the same time on an unknown email as on a
// known one. Without it, response timing reveals which addresses have accounts.
var dummyHash string

func init() {
	// Built once at startup; the cost is paid here rather than per request.
	h, err := HashPassword("aic-dummy-password-for-timing-equalisation")
	if err != nil {
		panic("auth: dummy hash: " + err.Error())
	}
	dummyHash = h
}

// VerifyOrDummy runs a real verification when the user exists, and an
// equivalent-cost verification when they do not, so both paths take similar
// time. It always returns ErrInvalidCredentials on failure.
func VerifyOrDummy(encoded, password string) error {
	if encoded == "" {
		_ = VerifyPassword(dummyHash, password)
		return ErrInvalidCredentials
	}
	return VerifyPassword(encoded, password)
}

// SessionTokenLen is the entropy of a session token in bytes.
const SessionTokenLen = 32

// NewSessionToken returns a fresh opaque token and the digest to store. The
// plaintext is returned to the caller exactly once, for the cookie.
func NewSessionToken() (token, digest string, err error) {
	b := make([]byte, SessionTokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("auth: read token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// HashToken is the stored form of a session token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
