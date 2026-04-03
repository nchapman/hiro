package toolrules

import "strings"

const (
	// trailingSpaceStarLen is the length of the " *" suffix used for
	// optional trailing-wildcard detection.
	trailingSpaceStarLen = 2

	// escapeSeqLen is the byte length of a backslash escape sequence
	// (the backslash plus the escaped character).
	escapeSeqLen = 2
)

// MatchWildcard reports whether text matches pattern.
//
// Special characters:
//   - * matches zero or more of any character
//   - \* matches a literal asterisk
//   - \\ matches a literal backslash
//
// All other characters match themselves literally.
//
// As a convenience, a trailing " *" (space + sole wildcard) is optional,
// so "git *" matches both "git add" and bare "git". This only applies
// when the trailing * is the only unescaped wildcard in the pattern.
//
// Matching is purely lexical — there is no path normalization. A pattern
// like "/src/*" will match "/src/../etc/passwd" because * matches the
// literal string "../etc/passwd". Callers that use path patterns should
// normalize paths (e.g. filepath.Clean) before matching.
func MatchWildcard(pattern, text string) bool {
	if matchCore(pattern, text) {
		return true
	}
	// "git *" matches bare "git" — trailing " *" is optional when the
	// trailing * is the only unescaped wildcard in the pattern.
	if trailingSpaceStarOptional(pattern) {
		return matchCore(pattern[:len(pattern)-2], text)
	}
	return false
}

// trailingSpaceStarOptional reports whether the pattern ends with an
// unescaped " *" that is the sole wildcard, making it optional.
func trailingSpaceStarOptional(pattern string) bool {
	if !strings.HasSuffix(pattern, " *") {
		return false
	}
	// The * is escaped if preceded by an odd number of backslashes.
	n := 0
	for i := len(pattern) - trailingSpaceStarLen - 1; i >= 0 && pattern[i] == '\\'; i-- {
		n++
	}
	if n%2 != 0 {
		return false
	}
	// Only apply when it's the sole unescaped wildcard.
	return !containsUnescapedStar(pattern[:len(pattern)-2])
}

// containsUnescapedStar reports whether s contains an unescaped *.
func containsUnescapedStar(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			i++ // skip escaped character
			continue
		}
		if s[i] == '*' {
			return true
		}
	}
	return false
}

// matchCore implements the two-pointer wildcard matching algorithm with
// escape sequence support.
func matchCore(pattern, text string) bool {
	px, tx := 0, 0
	starPx, starTx := -1, -1

	for tx < len(text) {
		if px < len(pattern) {
			// Wildcard: remember backtrack point.
			if pattern[px] == '*' {
				starPx, starTx = px, tx
				px++
				continue
			}

			// Resolve the pattern character (handling escapes).
			pc, advance := patternChar(pattern, px)

			if pc == text[tx] {
				px += advance
				tx++
				continue
			}
		}

		// Mismatch — backtrack to last wildcard.
		if starPx >= 0 {
			starTx++
			tx = starTx
			px = starPx + 1
			continue
		}
		return false
	}

	// Remaining pattern must be all wildcards.
	for px < len(pattern) {
		if pattern[px] != '*' {
			return false
		}
		px++
	}
	return true
}

// patternChar returns the literal character at position px in pattern,
// resolving escape sequences. Returns the character and how many bytes
// to advance past it.
func patternChar(pattern string, px int) (byte, int) {
	if pattern[px] == '\\' && px+1 < len(pattern) {
		return pattern[px+1], escapeSeqLen
	}
	return pattern[px], 1
}
