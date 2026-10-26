package api_test

import (
	"net/http"
	"testing"
)

// The company's language is a company setting: it travels with the tenant's
// view, and the update endpoint accepts it.
func TestTenantViewCarriesTheLanguage(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie

	tenant := h.do("GET", "/api/tenant", nil, owner).json(t)["tenant"].(map[string]any)
	if tenant["language"] != "en" {
		t.Errorf("a new company works in %v, want en", tenant["language"])
	}

	// The change is persisted, not merely echoed.
	res := h.do("PATCH", "/api/tenant", map[string]any{"language": "pt-BR"}, owner)
	if res.status != http.StatusOK {
		t.Fatalf("set the language: %d %s", res.status, res.body)
	}
	after := res.json(t)["tenant"].(map[string]any)
	if after["language"] != "pt-BR" {
		t.Errorf("the response says %v, want pt-BR", after["language"])
	}
	reloaded := h.do("GET", "/api/tenant", nil, owner).json(t)["tenant"].(map[string]any)
	if reloaded["language"] != "pt-BR" {
		t.Errorf("the stored language is %v, want pt-BR", reloaded["language"])
	}
}

// An unknown language is refused with the house's error style, and the setting
// is left alone.
func TestTenantLanguageUpdateRefusesUnknownLanguages(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie

	res := h.do("PATCH", "/api/tenant", map[string]any{"language": "fr"}, owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("an unknown language was accepted: %d %s", res.status, res.body)
	}
	errorObj, _ := res.json(t)["error"].(map[string]any)
	if errorObj["code"] != "invalid_request" {
		t.Errorf("code = %v, want invalid_request", errorObj["code"])
	}
	after := h.do("GET", "/api/tenant", nil, owner).json(t)["tenant"].(map[string]any)
	if after["language"] != "en" {
		t.Errorf("the refused change was applied: language = %v", after["language"])
	}
}

// The language is a settings surface, so it is gated like one: a member
// cannot change it even though the route lets them read it.
func TestTenantLanguageUpdateRequiresOwnerOrAdmin(t *testing.T) {
	oh := newOwnerHarness(t)
	h, owner := oh.harness, oh.ownerCookie
	tenantID := h.tenantID(owner)

	h.addMember(tenantID, "member@example.com", "member")
	memberCookie := h.loginTo("member@example.com", tenantID)

	res := h.do("PATCH", "/api/tenant", map[string]any{"language": "pt-BR"}, memberCookie)
	if res.status != http.StatusForbidden {
		t.Fatalf("a member changed the company's language: %d %s", res.status, res.body)
	}
	after := h.do("GET", "/api/tenant", nil, owner).json(t)["tenant"].(map[string]any)
	if after["language"] != "en" {
		t.Errorf("the refused change was applied: language = %v", after["language"])
	}
}
