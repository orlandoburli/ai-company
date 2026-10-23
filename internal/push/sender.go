package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/integration"
	"github.com/orlandoburli/ai-company/internal/tenancy"
	"golang.org/x/crypto/hkdf"
)

const vapidRef = "ai-company/push:vapid"

var ErrNotConfigured = errors.New("push is not configured: VAPID key unavailable")

// Sender delivers RFC 8291 encrypted Web Push payloads signed with a VAPID key.
type Sender struct {
	store       *Store
	client      *http.Client
	credentials integration.SecretSource
	logger      *slog.Logger
}

func NewSender(store *Store, log *slog.Logger) *Sender {
	if log == nil {
		log = slog.Default()
	}
	return &Sender{store: store, client: &http.Client{Timeout: 15 * time.Second}, credentials: integration.Credentials{}, logger: log}
}

// Notify sends the event to all tenant subscriptions. Gone endpoints are removed.
func (s *Sender) Notify(ctx context.Context, sc tenancy.Scope, title, body, link string) error {
	subs, err := s.store.ListAll(ctx, sc)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	key, err := s.vapidKey(ctx)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"title": title, "body": body, "link": link})
	if err != nil {
		return err
	}
	var errs []error
	for _, sub := range subs {
		if err := s.send(ctx, key, sub, payload); err != nil {
			var gone goneError
			if errors.As(err, &gone) {
				if e := s.removeGone(ctx, sc, sub.ID); e != nil {
					errs = append(errs, e)
				}
				continue
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Sender) removeGone(ctx context.Context, sc tenancy.Scope, id string) error {
	return s.store.withTenant(ctx, sc, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE id=$1`, id)
		return err
	})
}

// vapidKey resolves a keychain secret, creating a keypair in the macOS login
// keychain on first use. The private scalar is never stored in Postgres or logged.
func (s *Sender) vapidKey(ctx context.Context) (*ecdsa.PrivateKey, error) {
	secret, err := s.credentials.Resolve(ctx, vapidRef)
	if err != nil {
		if runtime.GOOS != "darwin" {
			return nil, ErrNotConfigured
		}
		key, genErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if genErr != nil {
			return nil, ErrNotConfigured
		}
		secret = base64.RawURLEncoding.EncodeToString(key.D.FillBytes(make([]byte, 32)))
		cmd := exec.CommandContext(ctx, "security", "add-generic-password", "-U", "-s", "ai-company", "-a", "push:vapid", "-w", secret)
		cmd.Env = integrationCredentialEnv()
		if _, e := cmd.CombinedOutput(); e != nil {
			return nil, ErrNotConfigured
		}
		return key, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(secret))
	if err != nil || len(decoded) != 32 {
		return nil, ErrNotConfigured
	}
	curve := elliptic.P256()
	d := new(big.Int).SetBytes(decoded)
	if d.Sign() == 0 || d.Cmp(curve.Params().N) >= 0 {
		return nil, ErrNotConfigured
	}
	x, y := curve.ScalarBaseMult(decoded)
	return &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: d}, nil
}

func (s *Sender) send(ctx context.Context, key *ecdsa.PrivateKey, sub Subscription, payload []byte) error {
	endpoint, err := url.Parse(sub.Endpoint)
	if err != nil || (endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "::1"))) || endpoint.Host == "" {
		return fmt.Errorf("invalid push endpoint")
	}
	body, err := encryptPayload(sub, payload)
	if err != nil {
		return err
	}
	jwt, err := vapidJWT(key, endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", "60")
	req.Header.Set("Urgency", "normal")
	pub := elliptic.Marshal(key.Curve, key.X, key.Y)
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+base64.RawURLEncoding.EncodeToString(pub))
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("push delivery failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return goneError{ID: sub.ID}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("push endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

type goneError struct{ ID string }

func (e goneError) Error() string { return "push subscription is gone" }

func vapidJWT(key *ecdsa.PrivateKey, endpoint *url.URL) (string, error) {
	claims, _ := json.Marshal(map[string]any{"aud": endpoint.Scheme + "://" + endpoint.Host, "exp": time.Now().Add(12 * time.Hour).Unix(), "sub": "mailto:push@ai-company.local"})
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	r, sigS, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		return "", err
	}
	sig := append(r.FillBytes(make([]byte, 32)), sigS.FillBytes(make([]byte, 32))...)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func encryptPayload(sub Subscription, plaintext []byte) ([]byte, error) {
	ua, err := decodeKey(sub.P256DH)
	if err != nil {
		return nil, ErrInvalid
	}
	peerX, peerY := elliptic.Unmarshal(elliptic.P256(), ua)
	if peerX == nil {
		return nil, ErrInvalid
	}
	auth, err := decodeKey(sub.Auth)
	if err != nil || len(auth) != 16 {
		return nil, ErrInvalid
	}
	eph, _, _, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	curve := elliptic.P256()
	ex, ey := curve.ScalarBaseMult(eph)
	asPub := elliptic.Marshal(curve, ex, ey)
	sharedX, _ := curve.ScalarMult(peerX, peerY, eph)
	shared := sharedX.FillBytes(make([]byte, 32))
	keyInfo := append([]byte("WebPush: info\x00"), ua...)
	keyInfo = append(keyInfo, asPub...)
	ikm := make([]byte, 32)
	if _, err = io.ReadFull(hkdf.New(sha256.New, shared, auth, keyInfo), ikm); err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return nil, err
	}
	cek, nonce := make([]byte, 16), make([]byte, 12)
	if _, err = io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek); err != nil {
		return nil, err
	}
	if _, err = io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	msg := append(append([]byte(nil), plaintext...), 0x02)
	ciphertext := gcm.Seal(nil, nonce, msg, nil)
	out := make([]byte, 0, 16+4+1+len(asPub)+len(ciphertext))
	out = append(out, salt...)
	var rs [4]byte
	binary.BigEndian.PutUint32(rs[:], 4096)
	out = append(out, rs[:]...)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	out = append(out, ciphertext...)
	return out, nil
}

func decodeKey(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func integrationCredentialEnv() []string {
	keys := []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "USER"}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
