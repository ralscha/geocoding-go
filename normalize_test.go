package geocode

import "testing"

func TestNormalize(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"", ""},
		{"bahnhofstrasse 10 zurich", "bahnhofstrasse 10 zurich"},
		{"  Bahnhofstrasse,\t10\r\nZurich!  ", "bahnhofstrasse 10 zurich"},
		{" \t\n---_!", ""},
		{"A\x00B\x7fC", "a b c"},
		{"Zürich Straße Æ Œ Ø Ł", "zurich strasse ae oe o l"},
		{"𝑨ＢＣ １２３", "abc 123"},
		{"東京 Москва", "東京 москва"},
		{"Z\u0308urich", "zurich"},
	} {
		if got := Normalize(test.input); got != test.want {
			t.Errorf("Normalize(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestNormalizeASCIICharacterClasses(t *testing.T) {
	for c := byte(0); c < 128; c++ {
		middle := c
		switch {
		case c >= 'A' && c <= 'Z':
			middle += 'a' - 'A'
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		default:
			middle = ' '
		}
		input := "A" + string(c) + "B"
		want := "a" + string(middle) + "b"
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}
