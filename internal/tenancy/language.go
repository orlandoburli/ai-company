package tenancy

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Language is the language a tenant's work is done in.
//
// A tenant declares it once; every run of the tenant inherits it, and every
// agent of that run writes its answers, its documents, its pull requests, its
// comments and its commit messages in it. The vocabulary is deliberately
// small: a language the product does not speak is refused, never guessed at.
type Language string

const (
	// LanguageEnglish is English.
	LanguageEnglish Language = "en"
	// LanguagePortugueseBrazil is Brazilian Portuguese.
	LanguagePortugueseBrazil Language = "pt-BR"
)

// DefaultLanguage is the language a tenant works in until it says otherwise.
const DefaultLanguage = LanguageEnglish

// Languages lists every language the product speaks, in the order the settings
// screen offers them.
var Languages = []Language{LanguageEnglish, LanguagePortugueseBrazil}

// Valid reports whether l is a language the product speaks.
func (l Language) Valid() bool {
	switch l {
	case LanguageEnglish, LanguagePortugueseBrazil:
		return true
	}
	return false
}

// Name returns the language's own name, the way the agents are told:
// "English" or "Portuguese (Brazil)". The name is spelled out rather than
// abbreviated to a tag, because it goes into a prompt.
func (l Language) Name() string {
	switch l {
	case LanguagePortugueseBrazil:
		return "Portuguese (Brazil)"
	case LanguageEnglish:
		return "English"
	}
	return string(l)
}

// ParseLanguage reads a language from a request or a setting. It reports
// whether the value is one the product speaks.
func ParseLanguage(value string) (Language, bool) {
	language := Language(strings.TrimSpace(value))
	return language, language.Valid()
}

// TenantLanguage reads the language the session's tenant works in.
//
// The read goes through the identity plane's function surface, keyed on the
// session token: the application role has no privileges on the tenants table,
// and the function cannot be pointed at another tenant.
func (s *Store) TenantLanguage(ctx context.Context, tokenHash string) (Language, error) {
	var language sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT public.auth_tenant_language($1)`, tokenHash).Scan(&language)
	if err != nil {
		return "", fmt.Errorf("tenant language: %w", err)
	}
	// The function returns NULL for a token that resolves to no session.
	if !language.Valid || strings.TrimSpace(language.String) == "" {
		return "", ErrNotFound
	}
	parsed, ok := ParseLanguage(language.String)
	if !ok {
		// The column is checked in the database, so this cannot happen. A
		// language the product does not speak is refused rather than guessed.
		return "", ErrInvalidLanguage
	}
	return parsed, nil
}

// SetTenantLanguage changes the language the tenant works in.
//
// The change is made by the identity plane's function surface, exactly like a
// rename: the role comes from the session's own membership, the tenant comes
// from the session, and an unknown language is refused.
func (s *Store) SetTenantLanguage(ctx context.Context, tokenHash string, language Language) error {
	if !language.Valid() {
		return ErrInvalidLanguage
	}
	var reason sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT public.auth_update_tenant_language($1, $2)`,
		tokenHash, string(language)).Scan(&reason); err != nil {
		return fmt.Errorf("set tenant language: %w", err)
	}
	return roleError(reason)
}
