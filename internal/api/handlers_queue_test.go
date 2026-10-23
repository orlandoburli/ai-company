package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/orlandoburli/ai-company/internal/store/postgres/pgtest"
)

type queueHarness struct {
	t      *testing.T
	db     *sql.DB
	admin  *pgtest.DB
	client *http.Client
	base   string
}
type queueResponse struct {
	status int
	body   []byte
	cookie string
}

func newQueueHarness(t *testing.T) *queueHarness {
	t.Helper()
	admin := pgtest.FreshDB(t)
	app := admin.AsRole(t, "aic_app")
	s := New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), WithSecureCookies(false))
	mux := http.NewServeMux()
	s.registerQueueRoutes(mux)
	mux.Handle("/", s.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &queueHarness{t: t, db: app, admin: admin, client: srv.Client(), base: srv.URL}
}
func (h *queueHarness) do(method, path string, body any, cookie string) queueResponse {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			h.t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.base+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: cookie})
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	out := queueResponse{status: resp.StatusCode, body: b}
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			out.cookie = c.Value
		}
	}
	return out
}
func (r queueResponse) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(r.body, &out); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return out
}
func (h *queueHarness) register(email, tenant, slug string) (cookie, user, tenantID string) {
	h.t.Helper()
	res := h.do("POST", "/api/auth/register", map[string]any{"email": email, "password": "correct-horse-battery-staple", "name": "Queue Test", "tenant_name": tenant, "tenant_slug": slug}, "")
	if res.status != http.StatusCreated {
		h.t.Fatalf("register: %d %s", res.status, res.body)
	}
	root := res.json(h.t)
	return res.cookie, root["user"].(map[string]any)["id"].(string), root["tenant"].(map[string]any)["id"].(string)
}

func TestQueueEndpointsKeepContractAndEnforceRoles(t *testing.T) {
	h := newQueueHarness(t)
	cookie, _, tenantID := h.register("queue-owner@example.test", "Queue Owner", "queue-owner")
	product := h.do("POST", "/api/products", map[string]any{"name": "Product", "slug": "product"}, cookie)
	if product.status != http.StatusCreated {
		t.Fatalf("product: %d %s", product.status, product.body)
	}
	productID := product.json(t)["product"].(map[string]any)["id"].(string)
	project := h.do("POST", "/api/projects", map[string]any{"product_id": productID, "name": "Project"}, cookie)
	if project.status != http.StatusCreated {
		t.Fatalf("project: %d %s", project.status, project.body)
	}
	projectID := project.json(t)["project"].(map[string]any)["id"].(string)
	created := h.do("POST", "/api/items", map[string]any{"project_id": projectID, "kind": "bug", "title": "Dispatch me"}, cookie)
	if created.status != http.StatusCreated {
		t.Fatalf("item: %d %s", created.status, created.body)
	}
	item := created.json(t)["item"].(map[string]any)
	itemID := item["id"].(string)
	queue := h.do("GET", "/api/queue", nil, cookie)
	if queue.status != http.StatusOK {
		t.Fatalf("queue: %d %s", queue.status, queue.body)
	}
	if _, ok := queue.json(t)["queue"].([]any); !ok {
		t.Fatalf("queue contract changed: %s", queue.body)
	}
	run := h.do("POST", "/api/rankings/run", map[string]any{}, cookie)
	if run.status != http.StatusCreated {
		t.Fatalf("run ranking: %d %s", run.status, run.body)
	}
	rankingObj := run.json(t)["ranking"].(map[string]any)
	rankingID := rankingObj["id"].(string)
	if rankingObj["degraded_reason"] != "llm_failed" {
		t.Fatalf("missing explicit fallback: %v", rankingObj)
	}
	queue = h.do("GET", "/api/queue", nil, cookie)
	entry := queue.json(t)["queue"].([]any)[0].(map[string]any)
	if entry["rank"] != float64(1) {
		t.Fatalf("queue omitted ranking rank: %v", entry)
	}
	start := h.do("POST", "/api/items/"+itemID+"/start-now", nil, cookie)
	if start.status != http.StatusAccepted {
		t.Fatalf("start-now: %d %s", start.status, start.body)
	}
	if res := h.do("GET", "/api/items/"+itemID+"/failure", nil, cookie); res.status != http.StatusBadRequest {
		t.Fatalf("missing workflow id status=%d body=%s", res.status, res.body)
	}
	if res := h.do("GET", "/api/rankings/"+rankingID, nil, cookie); res.status != http.StatusOK {
		t.Fatalf("ranking detail: %d %s", res.status, res.body)
	}
	if res := h.do("GET", "/api/rankings", nil, cookie); res.status != http.StatusOK || len(res.json(t)["rankings"].([]any)) != 1 {
		t.Fatalf("ranking list: %d %s", res.status, res.body)
	}
	_, guestUser, _ := h.register("queue-guest@example.test", "Queue Guest", "queue-guest")
	if _, err := h.admin.Exec(`INSERT INTO tenant_memberships(id,tenant_id,user_id,role) VALUES($1,$2,$3,'guest')`, "guest-queue-membership", tenantID, guestUser); err != nil {
		t.Fatal(err)
	}
	login := h.do("POST", "/api/auth/login", map[string]any{"email": "queue-guest@example.test", "password": "correct-horse-battery-staple", "tenant_id": tenantID}, "")
	if login.status != http.StatusOK {
		t.Fatalf("guest login: %d %s", login.status, login.body)
	}
	denied := h.do("POST", "/api/items/"+itemID+"/start-now", nil, login.cookie)
	if denied.status != http.StatusForbidden {
		t.Fatalf("guest start-now status=%d body=%s", denied.status, denied.body)
	}
	if got := h.do("GET", "/api/rankings/"+rankingID, nil, login.cookie); got.status != http.StatusOK {
		t.Fatalf("guest read status=%d body=%s", got.status, got.body)
	}
	otherCookie, _, _ := h.register("queue-other@example.test", "Other Tenant", "queue-other")
	if crossTenant := h.do("GET", "/api/rankings/"+rankingID, nil, otherCookie); crossTenant.status != http.StatusNotFound {
		t.Fatalf("cross-tenant ranking status=%d body=%s", crossTenant.status, crossTenant.body)
	}
}
