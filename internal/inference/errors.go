package inference

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"charm.land/fantasy"
)

var (
	errRequestCancelled = errors.New("request cancelled")
	errContextTooLarge  = errors.New("context too large for this model — use /clear to start a fresh session")
)

// formatInferenceError converts a raw inference error into a user-friendly
// message. It unwraps RetryError and ProviderError to extract structured
// information, and provides actionable guidance for known failure modes.
func formatInferenceError(err error) error {
	if errors.Is(err, context.Canceled) {
		return errRequestCancelled
	}

	pe := unwrapProviderError(err)
	if pe != nil {
		// Use the explicit flag only — IsContextTooLarge() also fires on
		// non-zero token counts that some providers attach to every response.
		if pe.ContextTooLargeErr {
			return errContextTooLarge
		}
		title := inferenceErrorTitle(pe)
		if pe.Message != "" {
			return fmt.Errorf("%s: %s", title, pe.Message)
		}
		return errors.New(title)
	}

	var fantasyErr *fantasy.Error
	if errors.As(err, &fantasyErr) {
		if fantasyErr.Title != "" && fantasyErr.Message != "" {
			return fmt.Errorf("%s: %s", fantasyErr.Title, fantasyErr.Message)
		}
		if fantasyErr.Message != "" {
			return errors.New(fantasyErr.Message)
		}
		if fantasyErr.Title != "" {
			return errors.New(fantasyErr.Title)
		}
	}

	return fmt.Errorf("inference failed: %w", err)
}

// inferenceErrorTitle returns a short human-readable title for a ProviderError.
func inferenceErrorTitle(pe *fantasy.ProviderError) string {
	if pe.Title != "" {
		return pe.Title
	}
	if pe.StatusCode > 0 {
		return statusTitle(pe.StatusCode)
	}
	return "Provider error"
}

// unwrapProviderError extracts a ProviderError from err. errors.As handles
// traversal through RetryError.Unwrap() and fmt.Errorf wrapping.
func unwrapProviderError(err error) *fantasy.ProviderError {
	var pe *fantasy.ProviderError
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}

// statusTitle returns a sentence-case title for HTTP status codes.
// Custom labels for codes where the standard text is unhelpful (e.g.
// "Unauthorized" → "Authentication failed"); falls back to sentence-cased
// http.StatusText for everything else.
func statusTitle(code int) string {
	switch code {
	case http.StatusTooManyRequests:
		return "Rate limited"
	case http.StatusUnauthorized:
		return "Authentication failed"
	case http.StatusForbidden:
		return "Access denied"
	case http.StatusInternalServerError:
		return "Server error"
	default:
		if text := http.StatusText(code); text != "" {
			return strings.ToUpper(text[:1]) + strings.ToLower(text[1:])
		}
		return fmt.Sprintf("HTTP %d", code)
	}
}
