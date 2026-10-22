package geocode

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Normalize is shared by indexing and querying. It retains non-Latin scripts,
// folds accents/case, and treats punctuation as token boundaries.
func Normalize(s string) string {
	// Most address numbers and many queries are ASCII. Avoid Unicode
	// decomposition, and return already-normalized text without allocating.
	ascii, normalized, space := true, true, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= utf8.RuneSelf {
			ascii = false
			break
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			space = false
		} else {
			if c != ' ' || space || i == len(s)-1 {
				normalized = false
			}
			space = true
		}
	}
	if ascii {
		if normalized {
			return s
		}
		var b strings.Builder
		b.Grow(len(s))
		space = true
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
				b.WriteByte(c)
				space = false
			} else if !space {
				b.WriteByte(' ')
				space = true
			}
		}
		return strings.TrimSpace(b.String())
	}
	s = norm.NFKD.String(strings.ToLower(s))
	var b strings.Builder
	b.Grow(len(s))
	space = true
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
