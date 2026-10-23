// Package integration implements narrowly-scoped external integration clients.
package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

var ErrNoSecret = errors.New("integration: secret not found")

// SecretSource resolves keychain references. Secret values are returned only to
// the caller and are never included in errors.
type SecretSource interface {
	Resolve(context.Context, string) (string, error)
}

// Credentials reads secrets from a test/CI override or, on macOS, the login
// keychain. The value remains in process memory only.
type Credentials struct {
	LookupEnv func(string) (string, bool)
	GOOS      string
}

func (c Credentials) Resolve(ctx context.Context, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", fmt.Errorf("%w: %q", ErrNoSecret, ref)
	}
	lookup := c.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	key := "AIC_SECRET_" + secretEnvName(ref)
	if secret, ok := lookup(key); ok && secret != "" {
		return secret, nil
	}
	goos := c.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "darwin" {
		parts := strings.SplitN(ref, "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-w", "-s", parts[0], "-a", parts[1])
			cmd.Env = credentialEnv()
			out, err := cmd.Output()
			if err == nil && strings.TrimSpace(string(out)) != "" {
				return strings.TrimRight(string(out), "\r\n"), nil
			}
		}
	}
	return "", fmt.Errorf("%w: %q", ErrNoSecret, ref)
}

func secretEnvName(ref string) string {
	var b strings.Builder
	for _, r := range ref {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Redact removes a known secret from text. The empty secret is ignored.
func Redact(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[REDACTED]")
}

func credentialEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "USER"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}
