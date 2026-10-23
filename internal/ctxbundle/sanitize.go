package ctxbundle

import (
	"regexp"
	"strings"
)

var (
	htmlTagRE     = regexp.MustCompile(`<(/?[A-Za-z][^<>]*)>`)
	commandLineRE = regexp.MustCompile(`(?im)(^|\n)([ \t]*)(ignore[ \t]+previous[ \t]+instructions|system[ \t]*:|assistant[ \t]*:)`)
)

// SanitizeUntrusted removes terminal/control and invisible directional
// characters, and neutralizes markup or instruction-shaped lines while
// retaining their readable text. Notes are stable, de-duplicated descriptions
// of the transformations performed.
func SanitizeUntrusted(s string) (string, []string) {
	runes := []rune(s)
	var clean strings.Builder
	notes := make([]string, 0, 6)
	addNote := func(note string) {
		for _, previous := range notes {
			if previous == note {
				return
			}
		}
		notes = append(notes, note)
	}
	for i := 0; i < len(runes); {
		r := runes[i]
		if r == 0x1b {
			if i+1 < len(runes) && runes[i+1] == '[' {
				end := i + 2
				for end < len(runes) && !(runes[end] >= 0x40 && runes[end] <= 0x7e) {
					end++
				}
				if end < len(runes) {
					end++
				}
				i = end
				addNote("stripped ANSI CSI sequence")
				continue
			}
			if i+1 < len(runes) && runes[i+1] == ']' {
				i = skipOSC(runes, i+2)
				addNote("stripped ANSI OSC sequence")
				continue
			}
			if i+1 < len(runes) {
				i += 2
			} else {
				i++
			}
			addNote("stripped ANSI escape sequence")
			continue
		}
		if r == 0x9b {
			end := i + 1
			for end < len(runes) && !(runes[end] >= 0x40 && runes[end] <= 0x7e) {
				end++
			}
			if end < len(runes) {
				end++
			}
			i = end
			addNote("stripped ANSI CSI sequence")
			continue
		}
		if r == 0x9d {
			i = skipOSC(runes, i+1)
			addNote("stripped ANSI OSC sequence")
			continue
		}
		if r == 0 || (r < 0x20 && r != '\n' && r != '\t') || (r >= 0x7f && r <= 0x9f) {
			i++
			if r == 0 {
				addNote("stripped NUL byte")
			} else {
				addNote("stripped control character")
			}
			continue
		}
		if (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			i++
			addNote("stripped bidi override/isolate character")
			continue
		}
		if r == 0x200b || r == 0x200c || r == 0x200d || r == 0x2060 || r == 0xfeff {
			i++
			addNote("stripped zero-width character")
			continue
		}
		clean.WriteRune(r)
		i++
	}

	text := clean.String()
	if strings.Contains(text, "<<<") || strings.Contains(text, ">>>") {
		text = strings.ReplaceAll(text, "<<<", "&lt;&lt;&lt;")
		text = strings.ReplaceAll(text, ">>>", "&gt;&gt;&gt;")
		addNote("neutralized framing delimiter")
	}
	if htmlTagRE.MatchString(text) {
		text = htmlTagRE.ReplaceAllString(text, "&lt;$1&gt;")
		addNote("neutralized HTML tag")
	}
	if strings.Contains(text, "```") || strings.Contains(text, "~~~") {
		text = strings.ReplaceAll(text, "```", "｀｀｀")
		text = strings.ReplaceAll(text, "~~~", "～～～")
		addNote("neutralized markdown fence")
	}
	if commandLineRE.MatchString(text) {
		text = commandLineRE.ReplaceAllString(text, "$1$2> DATA: $3")
		addNote("neutralized instruction-shaped line")
	}
	return text, notes
}

func skipOSC(runes []rune, start int) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == '\a' || runes[i] == 0x9c {
			return i + 1
		}
		if runes[i] == 0x1b && i+1 < len(runes) && runes[i+1] == '\\' {
			return i + 2
		}
	}
	return len(runes)
}
