package textkey

import "testing"

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"Árvíztűrő Tükörfúrógép": "arvizturo tukorfurogep",
		"  Őrült   világ ":       "orult vilag",
		"ŐŰ öü":                  "ou ou",
		"Čapek Škoda":            "capek skoda",
		"":                       "",
		"ABC":                    "abc",
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortKeyIgnoresLeadingPunctuation(t *testing.T) {
	for in, want := range map[string]string{"(versek)": "versek)", `"Ünnep"`: "unnep\"", "...és még": "es meg", "123 film": "123 film"} {
		if got := SortKey(in); got != want {
			t.Errorf("SortKey(%q) = %q, want %q", in, got, want)
		}
	}
	if SortKey("Ábel") >= SortKey("Béla") || SortKey("Zoltán") <= SortKey("Ödön") {
		t.Error("accented letters must sort with their base letter")
	}
}

func TestEscapeLike(t *testing.T) {
	if got := EscapeLike(`50%_\x`); got != `50\%\_\\x` {
		t.Fatal(got)
	}
}
