package tenancy_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// The vocabulary is exactly the two languages the database check allows, and
// nothing else is accepted.
func TestLanguageVocabulary(t *testing.T) {
	if len(tenancy.Languages) != 2 {
		t.Fatalf("the product speaks %d languages, want 2: %v", len(tenancy.Languages), tenancy.Languages)
	}
	want := map[tenancy.Language]string{
		tenancy.LanguageEnglish:          "English",
		tenancy.LanguagePortugueseBrazil: "Portuguese (Brazil)",
	}
	for _, language := range tenancy.Languages {
		if !language.Valid() {
			t.Errorf("%q is listed but not valid", language)
		}
		if language.Name() != want[language] {
			t.Errorf("%q is named %q, want %q", language, language.Name(), want[language])
		}
	}
	if tenancy.LanguageEnglish.Name() != "English" || tenancy.LanguagePortugueseBrazil.Name() != "Portuguese (Brazil)" {
		t.Error("the languages are not named the way the agents are told")
	}
	if _, ok := tenancy.ParseLanguage("pt-BR"); !ok {
		t.Error("pt-BR was refused")
	}
	if _, ok := tenancy.ParseLanguage("fr"); ok {
		t.Error("fr was accepted")
	}
}

// The company's language round-trips: it is stored, it is read back, and the
// only door to it is the identity plane's function surface.
func TestTenantLanguageRoundTrip(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	owner, m := register(t, s, "owner@example.com", "Owner", "Acme", "acme")
	if _, err := s.CreateSession(ctx, owner.UserID, m.TenantID, "tok-owner", time.Hour); err != nil {
		t.Fatalf("owner session: %v", err)
	}

	// A company works in English until it says otherwise.
	language, err := s.TenantLanguage(ctx, "tok-owner")
	if err != nil {
		t.Fatalf("read the tenant language: %v", err)
	}
	if language != tenancy.LanguageEnglish {
		t.Errorf("default language = %q, want %q", language, tenancy.LanguageEnglish)
	}

	// And the change is persisted, not merely echoed.
	if err := s.SetTenantLanguage(ctx, "tok-owner", tenancy.LanguagePortugueseBrazil); err != nil {
		t.Fatalf("set the tenant language: %v", err)
	}
	language, err = s.TenantLanguage(ctx, "tok-owner")
	if err != nil {
		t.Fatalf("read the tenant language: %v", err)
	}
	if language != tenancy.LanguagePortugueseBrazil {
		t.Errorf("language after the change = %q, want %q", language, tenancy.LanguagePortugueseBrazil)
	}
	var stored string
	if err := db.QueryRow(`SELECT language FROM tenants WHERE id = $1`, m.TenantID).Scan(&stored); err != nil {
		t.Fatalf("read the stored row: %v", err)
	}
	if stored != "pt-BR" {
		t.Errorf("stored language = %q, want pt-BR", stored)
	}
}

// An unknown language is refused, never ignored: a company that asked for a
// language the product does not speak must be told, not quietly given English.
func TestTenantLanguageRefusesUnknownLanguages(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	owner, m := register(t, s, "owner@example.com", "Owner", "Acme", "acme")
	if _, err := s.CreateSession(ctx, owner.UserID, m.TenantID, "tok-owner", time.Hour); err != nil {
		t.Fatalf("owner session: %v", err)
	}

	if err := s.SetTenantLanguage(ctx, "tok-owner", tenancy.Language("fr")); !errors.Is(err, tenancy.ErrInvalidLanguage) {
		t.Errorf("unknown language = %v, want ErrInvalidLanguage", err)
	}

	// The database refuses it too, whatever reaches it: the function answers
	// with a reason instead of changing anything.
	var reason string
	if err := db.QueryRow(`SELECT public.auth_update_tenant_language('tok-owner', 'fr')`).Scan(&reason); err != nil {
		t.Fatalf("call the function: %v", err)
	}
	if reason != "invalid_language" {
		t.Errorf("the function answered %q, want invalid_language", reason)
	}
}

// The language is a company setting, so it is authorized like one: an owner
// or an admin may change it and nobody else may.
func TestTenantLanguageIsAuthorizedLikeTheOtherSettings(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	owner, m := register(t, s, "owner@example.com", "Owner", "Acme", "acme")
	second, _ := register(t, s, "member@example.com", "Member", "Their Own Co", "their-own-co")
	if _, err := db.Exec(
		`INSERT INTO tenant_memberships (id, tenant_id, user_id, role) VALUES ($1, $2, $3, 'member')`,
		"invite-lang", m.TenantID, second.UserID); err != nil {
		t.Fatalf("invite: %v", err)
	}
	if _, err := s.CreateSession(ctx, second.UserID, m.TenantID, "tok-member", time.Hour); err != nil {
		t.Fatalf("member session: %v", err)
	}
	if _, err := s.CreateSession(ctx, owner.UserID, m.TenantID, "tok-owner", time.Hour); err != nil {
		t.Fatalf("owner session: %v", err)
	}

	// A member cannot change it, even calling the function directly.
	if err := s.SetTenantLanguage(ctx, "tok-member", tenancy.LanguagePortugueseBrazil); !errors.Is(err, tenancy.ErrForbiddenRole) {
		t.Errorf("member SetTenantLanguage = %v, want ErrForbiddenRole", err)
	}
	// Nor can a token that was never issued.
	if err := s.SetTenantLanguage(ctx, "tok-forged", tenancy.LanguagePortugueseBrazil); !errors.Is(err, tenancy.ErrUnauthenticated) {
		t.Errorf("forged token = %v, want ErrUnauthenticated", err)
	}
	// The owner can, which is the control proving the refusals above are
	// authorisation and not a broken function.
	if err := s.SetTenantLanguage(ctx, "tok-owner", tenancy.LanguagePortugueseBrazil); err != nil {
		t.Errorf("owner SetTenantLanguage: %v", err)
	}
}

// The language is the company's, not the session's: one session reads the
// language of the tenant it acts in and never of another one.
func TestTenantLanguageIsReadThroughTheSession(t *testing.T) {
	db := freshDB(t)
	s := appStore(t, db)
	ctx := context.Background()

	owner, m := register(t, s, "owner@example.com", "Owner", "Acme", "acme")
	if _, err := s.CreateSession(ctx, owner.UserID, m.TenantID, "tok-owner", time.Hour); err != nil {
		t.Fatalf("owner session: %v", err)
	}
	if err := s.SetTenantLanguage(ctx, "tok-owner", tenancy.LanguagePortugueseBrazil); err != nil {
		t.Fatalf("set the language: %v", err)
	}

	// Another company's session reads its own language, not this one's.
	other, om := register(t, s, "other@example.com", "Other", "Globex", "globex")
	if _, err := s.CreateSession(ctx, other.UserID, om.TenantID, "tok-other", time.Hour); err != nil {
		t.Fatalf("other session: %v", err)
	}
	language, err := s.TenantLanguage(ctx, "tok-other")
	if err != nil {
		t.Fatalf("read the other tenant's language: %v", err)
	}
	if language != tenancy.LanguageEnglish {
		t.Errorf("the other company reads %q, want %q", language, tenancy.LanguageEnglish)
	}

	// And a token that was never issued resolves to nothing at all.
	if _, err := s.TenantLanguage(ctx, "tok-forged"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Errorf("forged token = %v, want ErrNotFound", err)
	}
}
