// Package progid validates M3 program identifiers (e.g. M3-59945851A60999998).
//
// Every identifier that reaches a database query, an upstream URL or a file name
// must pass Valid first. The accepted alphabet is deliberately tiny.
package progid

import "strings"

const (
	prefix = "M3-"
	// MaxLen is the longest accepted identifier (the old schema allowed 31).
	MaxLen = 30
)

// Valid reports whether id is exactly "M3-" followed by 1..27 characters of A-Z or 0-9.
func Valid(id string) bool {
	if len(id) <= len(prefix) || len(id) > MaxLen || !strings.HasPrefix(id, prefix) {
		return false
	}
	for i := len(prefix); i < len(id); i++ {
		c := id[i]
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// ParseList splits a comma separated, user typed list. The input is case-insensitive;
// the result holds the unique valid identifiers in canonical (upper case) form, at most limit.
func ParseList(s string, limit int) []string {
	var out []string
	seen := make(map[string]bool)
	for _, part := range strings.Split(s, ",") {
		id := strings.ToUpper(strings.TrimSpace(part))
		if Valid(id) && !seen[id] {
			seen[id] = true
			out = append(out, id)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

// LooksLikeIDQuery reports whether a search term should be treated as an identifier lookup.
func LooksLikeIDQuery(s string) bool {
	return len(s) > len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
