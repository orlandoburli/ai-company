package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// ErrUnknownLanguage is returned when a request names a language the product
// does not speak.
//
// An unknown language is refused, never ignored: a run that quietly fell back
// to English would answer in a language nobody asked for, and the person who
// reads the answer would have no way of knowing.
var ErrUnknownLanguage = errors.New("workflow: unknown language")

// ResolveLanguage resolves the language a run works in.
//
// The request may name one — a person starting a run in Portuguese for a
// colleague who reads English — and the tenant's language is the default
// otherwise. Both are refused when they are not a language the product
// speaks.
func ResolveLanguage(requested, tenantLanguage string) (tenancy.Language, error) {
	if requested = strings.TrimSpace(requested); requested != "" {
		language, ok := tenancy.ParseLanguage(requested)
		if !ok {
			return "", fmt.Errorf("%w: %q", ErrUnknownLanguage, requested)
		}
		return language, nil
	}
	if tenantLanguage = strings.TrimSpace(tenantLanguage); tenantLanguage != "" {
		language, ok := tenancy.ParseLanguage(tenantLanguage)
		if !ok {
			return "", fmt.Errorf("%w: %q", ErrUnknownLanguage, tenantLanguage)
		}
		return language, nil
	}
	return tenancy.DefaultLanguage, nil
}

// LanguageInstruction is the directive that tells an agent which language to
// write in.
//
// It names the language explicitly and says what the language covers — the
// answers, the documents, the pull-request title and body, the comments and
// the commit messages — because an agent that writes its summary in one
// language and its commit messages in another has not been told anything
// useful.
func LanguageInstruction(language tenancy.Language) string {
	if !language.Valid() {
		language = tenancy.DefaultLanguage
	}
	name := language.Name()
	return fmt.Sprintf(`## Language

Your work language is %[1]s. Write EVERYTHING you produce in %[1]s:
- your answers, your summaries and your reports;
- the documents you write;
- the pull-request title and body;
- the comments you leave on issues and pull requests;
- your commit messages.

Do not write them in another language, not even when the request, the issue or
the code comments are written in one. Code, identifiers, file names, command
lines and quoted text stay exactly as they are.`, name)
}

// soulWithLanguage returns the agent's soul with the language directive
// attached.
//
// The directive lives in the soul because the soul is the trusted part of the
// context: the work item's text is data and must not be able to change the
// language of the answer, and the budget cannot trim the soul away either.
//
// The fingerprint the manifest carries covers the soul as it was used — the
// persona plus the directive — so the manifest says exactly which context was
// built.
func soulWithLanguage(soul string, language tenancy.Language) string {
	directive := LanguageInstruction(language)
	if strings.TrimSpace(soul) == "" {
		return directive
	}
	return strings.TrimRight(soul, "\n") + "\n\n" + directive
}
