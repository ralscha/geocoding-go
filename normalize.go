package geocode

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Normalize is shared by indexing and querying. It retains non-Latin scripts,
// folds accents/case, and treats punctuation as token boundaries.
func Normalize(s string) string {
	s = norm.NFKD.String(strings.ToLower(s))
	var b strings.Builder
	b.Grow(len(s))
	space := true
	for _, r := range s {
		r = unicode.ToLower(r) // Compatibility decomposition can expose capitals.
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			switch r {
			case 'ß':
				b.WriteString("ss")
			case 'æ':
				b.WriteString("ae")
			case 'œ':
				b.WriteString("oe")
			case 'ø':
				b.WriteByte('o')
			case 'ł':
				b.WriteByte('l')
			default:
				b.WriteRune(r)
			}
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}
