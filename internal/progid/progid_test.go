package progid

import (
	"reflect"
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := map[string]bool{
		"M3-59945851A60999998":          true,
		"M3-A":                          true,
		"M3-" + strings.Repeat("A", 27): true,
		"M3-" + strings.Repeat("A", 28): false,
		"M3-":                           false,
		"":                              false,
		"m3-59945851A60999998":          false, // canonical form only
		"M3-X') OR 1=1 --":              false,
		"M3-A'B":                        false,
		"M3-A&x=1":                      false,
		"M3-A/../../etc/passwd":         false,
		"M3-A\x00B":                     false,
		"M3-A\nB":                       false,
		"M3-ÁÉ":                         false,
		"M3-A B":                        false,
		"M3-A.jpg":                      false,
		"X3-ABC":                        false,
	}
	for in, want := range cases {
		if got := Valid(in); got != want {
			t.Errorf("Valid(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseList(t *testing.T) {
	got := ParseList("m3-aaa1, M3-BBB2 ,M3-AAA1,hello,M3-X') OR 1=1 --,,", 10)
	if want := []string{"M3-AAA1", "M3-BBB2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if got := ParseList("M3-A1,M3-A2,M3-A3", 2); len(got) != 2 {
		t.Fatalf("limit not applied: %v", got)
	}
	if got := ParseList("", 5); len(got) != 0 {
		t.Fatalf("empty input: %v", got)
	}
}

func TestLooksLikeIDQuery(t *testing.T) {
	for in, want := range map[string]bool{"M3-A": true, "m3-a": true, "M3-": false, "M3": false, "film": false, "xM3-A": false} {
		if got := LooksLikeIDQuery(in); got != want {
			t.Errorf("LooksLikeIDQuery(%q) = %v, want %v", in, got, want)
		}
	}
}
