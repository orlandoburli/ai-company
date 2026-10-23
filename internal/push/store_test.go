package push_test

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/crypto/hkdf"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orlandoburli/ai-company/internal/push"
	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type pushFixture struct {
	app    *sql.DB
	store  *push.Store
	t1, t2 tenancy.Scope
}

func newPushFixture(t *testing.T) *pushFixture {
	t.Helper()
	db := pgtest.FreshDB(t)
	app := db.AsRole(t, "aic_app")
	for _, id := range []string{"P1", "P2"} {
		if _, err := app.Exec(`SELECT auth_register_tenant($1,$2,$3,$4,$5,$6,$7,$8)`, id, "Tenant "+id, strings.ToLower(id), "U-"+id, strings.ToLower(id)+"@example.com", "x", "User "+id, "M-"+id); err != nil {
			t.Fatal(err)
		}
	}
	return &pushFixture{app: app, store: push.NewStore(app), t1: tenancy.Scope{TenantID: "P1", UserID: "U-P1", Role: tenancy.RoleOwner}, t2: tenancy.Scope{TenantID: "P2", UserID: "U-P2", Role: tenancy.RoleOwner}}
}

func TestStoreSubscribeResubscribeAndTenantIsolation(t *testing.T) {
	f := newPushFixture(t)
	ctx := context.Background()
	in := push.Subscription{UserID: f.t1.UserID, Endpoint: "https://push.example/sub/one", P256DH: "public1", Auth: "auth1"}
	a, err := f.store.Subscribe(ctx, f.t1, in)
	if err != nil {
		t.Fatal(err)
	}
	in.P256DH = "public2"
	b, err := f.store.Subscribe(ctx, f.t1, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || b.P256DH != "public2" {
		t.Fatalf("resubscription did not update existing endpoint: a=%+v b=%+v", a, b)
	}
	foreign, err := f.store.Subscribe(ctx, f.t2, push.Subscription{UserID: f.t2.UserID, Endpoint: "https://push.example/sub/two", P256DH: "p", Auth: "a"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.store.ListAll(ctx, f.t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != a.ID {
		t.Fatalf("tenant leaked subscription: %+v", rows)
	}
	owned, err := f.store.List(ctx, f.t1, f.t1.UserID)
	if err != nil || len(owned) != 1 {
		t.Fatalf("own subscriptions=%+v err=%v", owned, err)
	}
	if err := f.store.Unsubscribe(ctx, f.t1, foreign.ID); err != push.ErrNotFound {
		t.Fatalf("cross-tenant unsubscribe err=%v", err)
	}
	if err := f.store.Unsubscribe(ctx, f.t1, a.ID); err != nil {
		t.Fatal(err)
	}
	rows, err = f.store.ListAll(ctx, f.t1)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unsubscribe rows=%+v err=%v", rows, err)
	}
}

func TestSenderPostsEncryptedPayloadAndExpiresGoneEndpoint(t *testing.T) {
	f := newPushFixture(t)
	ctx := context.Background()
	browserPriv, browserX, browserY, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub := elliptic.Marshal(elliptic.P256(), browserX, browserY)
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	var got int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got++
		if r.Method != "POST" || r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") != "60" || r.Header.Get("Urgency") != "normal" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			t.Errorf("unexpected web-push headers: %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) < 100 {
			t.Errorf("missing encrypted payload: %d bytes", len(body))
		} else {
			plain, err := decryptForTest(body, browserPriv, pub, auth)
			if err != nil {
				t.Errorf("decrypt encrypted payload: %v", err)
			} else {
				var payload map[string]string
				if err := json.Unmarshal(plain, &payload); err != nil || payload["title"] != "Title" || payload["body"] != "Body" || payload["link"] != "/link" {
					t.Errorf("payload=%s err=%v", plain, err)
				}
			}
		}
		if r.URL.Path == "/gone" {
			w.WriteHeader(http.StatusGone)
		}
	}))
	defer srv.Close()
	for _, path := range []string{"active", "gone"} {
		_, err := f.store.Subscribe(ctx, f.t1, push.Subscription{UserID: f.t1.UserID, Endpoint: srv.URL + "/" + path, P256DH: base64.RawURLEncoding.EncodeToString(pub), Auth: base64.RawURLEncoding.EncodeToString(auth)})
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AIC_SECRET_AI_COMPANY_PUSH_VAPID", base64.RawURLEncoding.EncodeToString(make([]byte, 31))+"AQ")
	sender := push.NewSender(f.store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sender.Notify(ctx, f.t1, "Title", "Body", "/link"); err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("requests=%d, want 2", got)
	}
	rows, err := f.store.ListAll(ctx, f.t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !strings.HasSuffix(rows[0].Endpoint, "/active") {
		t.Fatalf("gone endpoint not removed: %+v", rows)
	}
}

func decryptForTest(body, browserPriv, browserPub, auth []byte) ([]byte, error) {
	if len(body) < 86 || int(body[20]) != 65 {
		return nil, io.ErrUnexpectedEOF
	}
	curve := elliptic.P256()
	asPub := body[21:86]
	x, y := elliptic.Unmarshal(curve, asPub)
	if x == nil {
		return nil, io.ErrUnexpectedEOF
	}
	sharedX, _ := curve.ScalarMult(x, y, browserPriv)
	shared := sharedX.FillBytes(make([]byte, 32))
	info := append([]byte("WebPush: info\x00"), browserPub...)
	info = append(info, asPub...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, auth, info), ikm); err != nil {
		return nil, err
	}
	cek, nonce := make([]byte, 16), make([]byte, 12)
	salt := body[:16]
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: aes128gcm\x00")), cek); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(hkdf.New(sha256.New, ikm, salt, []byte("Content-Encoding: nonce\x00")), nonce); err != nil {
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
	plain, err := gcm.Open(nil, nonce, body[86:], nil)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		return nil, io.ErrUnexpectedEOF
	}
	return plain[:len(plain)-1], nil
}
