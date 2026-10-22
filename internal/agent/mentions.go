package agent

import "regexp"

// mentionRE matches an @name mention that starts a word. The prefix class
// deliberately excludes letters, digits, '_', '@', '.' and '+', so an email
// address ("dev@example.com") or a URL is not mistaken for a mention while
// "( @dev)" and "@dev," still are.
var mentionRE = regexp.MustCompile(`(?i)(^|[^a-z0-9_@.+-])@([a-z0-9][a-z0-9_-]{0,63})`)

// ParseMentions returns the distinct agent names mentioned in a message, in
// first-appearance order and lowercased (agent names are lowercase by schema).
// It is pure on purpose: the API resolves the names against the tenant's
// registry so an unknown mention can fail the request before anything is
// written.
func ParseMentions(body string) []string {
	matches := mentionRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := lowerASCII(m[2])
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
