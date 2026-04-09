package netiso

import "strings"

// MatchDomain reports whether the queried domain matches any entry in the allowlist.
//
// Matching rules:
//   - "github.com" matches exactly "github.com"
//   - "*.github.com" matches any subdomain: "api.github.com", "foo.bar.github.com"
//   - "*.github.com" does NOT match "github.com" itself
func MatchDomain(query string, allowlist []string) bool {
	query = strings.ToLower(query)
	for _, entry := range allowlist {
		entry = strings.ToLower(entry)
		if entry == "*" {
			return true
		}
		if entry == query {
			return true
		}
		// Wildcard: *.example.com matches any.example.com, deep.any.example.com.
		if len(entry) > 2 && entry[:2] == "*." {
			suffix := entry[1:] // ".example.com"
			if strings.HasSuffix(query, suffix) && len(query) > len(suffix) {
				return true
			}
		}
	}
	return false
}
