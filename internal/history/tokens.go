package history

// EstimateTokens returns an approximate token count for a string.
// Uses the ~4 characters per token heuristic, which is a reasonable
// average across English text for most LLM tokenizers.
func EstimateTokens(s string) int {
	n := len(s) / 4
	if n == 0 && len(s) > 0 {
		return 1
	}
	return n
}
