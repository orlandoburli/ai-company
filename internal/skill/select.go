package skill

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type SelectOptions struct {
	ProductID *string
	Task      string
	MaxAttach int
	Keys      []string
}

type Selected struct {
	Skill   Skill
	Version Version
	Ref     string
	Reason  string
	Score   float64
}

// Select chooses only enabled skills with an approved current revision. In
// automatic mode, lexical overlap is measured against each trusted description;
// task text is only the query, never a source of selectors or skill metadata.
// Explicit keys select by exact trusted slug. Ties prefer product-local skills,
// then slug and ID. A zero bound uses the registry default of three.
func Select(ctx context.Context, sc tenancy.Scope, store *Store, opts SelectOptions) ([]Selected, error) {
	maxAttach := opts.MaxAttach
	if maxAttach == 0 {
		maxAttach = 3
	}
	if maxAttach < 0 {
		return nil, ErrInvalid
	}
	productID := ""
	if opts.ProductID != nil {
		productID = *opts.ProductID
	}
	rows, err := store.List(ctx, sc, productID)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]struct{}, len(opts.Keys))
	for _, key := range opts.Keys {
		keys[key] = struct{}{}
	}
	explicit := len(keys) > 0
	taskTerms := terms(opts.Task)
	candidates := make([]Selected, 0)
	for _, item := range rows {
		if !item.Enabled {
			continue
		}
		score := 0.0
		reason := ""
		if explicit {
			if _, ok := keys[item.Slug]; !ok {
				continue
			}
			score = 1000
			reason = "explicit selector"
		} else {
			if len(taskTerms) == 0 {
				continue
			}
			descriptionTerms := terms(item.Description)
			matched := 0
			for term := range taskTerms {
				if _, ok := descriptionTerms[term]; ok {
					matched++
				}
			}
			if matched == 0 {
				continue
			}
			score = float64(matched) / float64(len(taskTerms))
			reason = "description matched task"
		}
		if item.ProductID != nil && *item.ProductID == productID {
			score += 0.000001 // scope preference is only a deterministic tie-breaker.
		}
		version, ok, err := store.CurrentVersion(ctx, sc, item.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, Selected{
			Skill: item, Version: version,
			Ref:    "skill:" + item.Slug + "@" + itoa(version.Version),
			Reason: reason, Score: score,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if math.Abs(candidates[i].Score-candidates[j].Score) > 1e-12 {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].Skill.Slug != candidates[j].Skill.Slug {
			return candidates[i].Skill.Slug < candidates[j].Skill.Slug
		}
		return candidates[i].Skill.ID < candidates[j].Skill.ID
	})
	if len(candidates) > maxAttach {
		candidates = candidates[:maxAttach]
	}
	return candidates, nil
}

func terms(text string) map[string]struct{} {
	result := make(map[string]struct{})
	var word strings.Builder
	flush := func() {
		if word.Len() >= 2 {
			result[strings.ToLower(word.String())] = struct{}{}
		}
		word.Reset()
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(unicode.ToLower(r))
		} else {
			flush()
		}
	}
	flush()
	return result
}

func itoa(n int) string {
	// Versions are positive and small; formatting this way avoids locale or
	// representation variance in the stable manifest reference.
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
