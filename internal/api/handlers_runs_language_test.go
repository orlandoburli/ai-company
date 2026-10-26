package api

import (
	"net/http"
	"testing"
)

// The run's wire shape carries the language, and the language is resolved when
// the run starts: the one the request names, the company's own when the
// request names none.
func TestRunCarriesTheResolvedLanguage(t *testing.T) {
	h := newRunsHarness(t)
	owner, _, _ := h.register("owner@example.com", "Acme", "acme")
	itemID := h.item(owner)
	templateID := h.publishedTemplate(owner, "Language flow")

	// Without a language in the request the run inherits the company's, which
	// is English until the company says otherwise.
	inherited := startRun(t, h, owner, templateID, itemID)
	if inherited["language"] != "en" {
		t.Errorf("the run's language = %v, want en", inherited["language"])
	}

	// The request may name one.
	override := h.do("POST", "/api/items/"+itemID+"/runs", map[string]any{"template_id": templateID, "language": "pt-BR"}, owner)
	if override.status != http.StatusAccepted {
		t.Fatalf("start a run in Portuguese: %d %s", override.status, override.body)
	}
	run := override.json(t)["run"].(map[string]any)
	if run["language"] != "pt-BR" {
		t.Errorf("the run's language = %v, want pt-BR", run["language"])
	}

	// The company's own language is what a run without a request language
	// inherits.
	if res := h.do("PATCH", "/api/tenant", map[string]any{"language": "pt-BR"}, owner); res.status != http.StatusOK {
		t.Fatalf("set the company language: %d %s", res.status, res.body)
	}
	inherited = startRun(t, h, owner, templateID, itemID)
	if inherited["language"] != "pt-BR" {
		t.Errorf("the run inherited %v, want pt-BR", inherited["language"])
	}

	// And the run keeps the language it started in, even though the company
	// changed afterwards.
	if res := h.do("PATCH", "/api/tenant", map[string]any{"language": "en"}, owner); res.status != http.StatusOK {
		t.Fatalf("change the company language: %d %s", res.status, res.body)
	}
	detail := h.do("GET", "/api/runs/"+run["id"].(string), nil, owner)
	if detail.status != http.StatusOK {
		t.Fatalf("read the run: %d %s", detail.status, detail.body)
	}
	after := detail.json(t)["run"].(map[string]any)
	if after["language"] != "pt-BR" {
		t.Errorf("the run's language changed to %v; a run keeps the language it started in", after["language"])
	}
}

// An unknown language is refused rather than silently ignored: a run that
// quietly fell back to English would answer in a language nobody asked for.
func TestRunRefusesAnUnknownLanguage(t *testing.T) {
	h := newRunsHarness(t)
	owner, _, _ := h.register("owner@example.com", "Acme", "acme")
	itemID := h.item(owner)
	templateID := h.publishedTemplate(owner, "Language flow")

	res := h.do("POST", "/api/items/"+itemID+"/runs", map[string]any{"template_id": templateID, "language": "fr"}, owner)
	if res.status != http.StatusBadRequest {
		t.Fatalf("an unknown language was accepted: %d %s", res.status, res.body)
	}
	if code := apiCode(t, res); code != "invalid_request" {
		t.Errorf("code = %v, want invalid_request", code)
	}
}
