// Package textnorm folds text for case- and diacritics-insensitive matching,
// so that "zruda" finds "zrůda" and "Melf's" finds "Melf’s".
package textnorm

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var punct = strings.NewReplacer(
	"’", "'", "‘", "'", "ʼ", "'",
	"“", `"`, "”", `"`, "„", `"`,
	"–", "-", "—", "-", " ", " ",
)

// Fold lowercases s, strips diacritics, unifies quote/dash variants and
// collapses whitespace.
func Fold(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	out = punct.Replace(strings.ToLower(out))
	return strings.Join(strings.Fields(out), " ")
}
