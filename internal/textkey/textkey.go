// Package textkey makes the comparison keys used for sorting by title and for the short
// (1-2 character) search: lower case, Hungarian and other common Latin diacritics folded,
// leading punctuation dropped.
package textkey

import (
	"strings"
	"unicode"
)

var folds = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ä': 'a', 'ã': 'a', 'å': 'a', 'ā': 'a', 'ă': 'a', 'ą': 'a',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e', 'ē': 'e', 'ě': 'e', 'ę': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i', 'ī': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'ő': 'o', 'õ': 'o', 'ø': 'o', 'ō': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u', 'ű': 'u', 'ū': 'u', 'ů': 'u',
	'ý': 'y', 'ÿ': 'y', 'ñ': 'n', 'ń': 'n', 'ň': 'n', 'ç': 'c', 'ć': 'c', 'č': 'c',
	'š': 's', 'ś': 's', 'ž': 'z', 'ź': 'z', 'ż': 'z', 'ř': 'r', 'ł': 'l', 'đ': 'd', 'ď': 'd', 'ť': 't',
}

// Fold lower-cases s, folds diacritics and collapses runs of white space.
func Fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range strings.ToLower(s) {
		if f, ok := folds[r]; ok {
			r = f
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// SortKey is Fold without the leading punctuation, so "(versek)" sorts under "v".
func SortKey(s string) string {
	return strings.TrimLeftFunc(Fold(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// EscapeLike escapes %, _ and \ for a LIKE pattern used with ESCAPE '\'.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
