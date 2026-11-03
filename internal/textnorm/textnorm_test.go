package textnorm

import "testing"

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"Zrůda":                 "zruda",
		"Melf’s Minute Meteors": "melf's minute meteors",
		"  ŽLUŤOUČKÝ   kůň  ":   "zlutoucky kun",
		"Příručka hráče":        "prirucka hrace",
		"„uvozovky“ – pomlčka":  `"uvozovky" - pomlcka`,
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}
