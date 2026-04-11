package inference

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"charm.land/fantasy"
)

func TestFormatInferenceError_ContextCanceled(t *testing.T) {
	err := formatInferenceError(context.Canceled)
	if err.Error() != "request cancelled" {
		t.Fatalf("unexpected: %s", err)
	}

	// Wrapped context.Canceled
	err = formatInferenceError(fmt.Errorf("something: %w", context.Canceled))
	if err.Error() != "request cancelled" {
		t.Fatalf("unexpected: %s", err)
	}
}

func TestFormatInferenceError_ProviderError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		expect string
	}{
		{
			name: "rate limited with title and message",
			err: &fantasy.ProviderError{
				Title:      "Rate Limited",
				Message:    "Too many requests, please slow down.",
				StatusCode: http.StatusTooManyRequests,
			},
			expect: "Rate Limited: Too many requests, please slow down.",
		},
		{
			name: "server error without title",
			err: &fantasy.ProviderError{
				Message:    "Internal server error",
				StatusCode: http.StatusInternalServerError,
			},
			expect: "Server error: Internal server error",
		},
		{
			name: "context too large",
			err: &fantasy.ProviderError{
				Title:              "Bad Request",
				Message:            "prompt is too long",
				StatusCode:         http.StatusBadRequest,
				ContextTooLargeErr: true,
			},
			expect: "context too large for this model — use /clear to start a fresh session",
		},
		{
			name: "token counts without explicit flag are not context-too-large",
			err: &fantasy.ProviderError{
				Title:             "Bad Request",
				Message:           "something else went wrong",
				StatusCode:        http.StatusBadRequest,
				ContextMaxTokens:  200000,
				ContextUsedTokens: 250000,
			},
			expect: "Bad Request: something else went wrong",
		},
		{
			name: "unauthorized",
			err: &fantasy.ProviderError{
				Message:    "Invalid API key",
				StatusCode: http.StatusUnauthorized,
			},
			expect: "Authentication failed: Invalid API key",
		},
		{
			name:   "provider error with title only",
			err:    &fantasy.ProviderError{Title: "Overloaded", StatusCode: 529},
			expect: "Overloaded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatInferenceError(tt.err)
			if got.Error() != tt.expect {
				t.Fatalf("expected %q, got %q", tt.expect, got.Error())
			}
		})
	}
}

func TestFormatInferenceError_RetryError(t *testing.T) {
	// RetryError wrapping a ProviderError — should unwrap and format.
	inner := &fantasy.ProviderError{
		Title:      "Service Unavailable",
		Message:    "The model is currently overloaded.",
		StatusCode: http.StatusServiceUnavailable,
	}
	err := &fantasy.RetryError{Errors: []error{
		&fantasy.ProviderError{StatusCode: http.StatusServiceUnavailable, Message: "first attempt"},
		inner,
	}}

	got := formatInferenceError(err)
	if got.Error() != "Service Unavailable: The model is currently overloaded." {
		t.Fatalf("unexpected: %s", got)
	}
}

func TestFormatInferenceError_RetryErrorContextTooLarge(t *testing.T) {
	inner := &fantasy.ProviderError{
		StatusCode:         http.StatusBadRequest,
		Message:            "prompt is too long",
		ContextTooLargeErr: true,
	}
	err := &fantasy.RetryError{Errors: []error{inner}}

	got := formatInferenceError(err)
	if got.Error() != "context too large for this model — use /clear to start a fresh session" {
		t.Fatalf("unexpected: %s", got)
	}
}

func TestFormatInferenceError_FantasyError(t *testing.T) {
	err := &fantasy.Error{Title: "Model Not Found", Message: "claude-99 does not exist"}
	got := formatInferenceError(err)
	if got.Error() != "Model Not Found: claude-99 does not exist" {
		t.Fatalf("unexpected: %s", got)
	}

	// Without title
	err = &fantasy.Error{Message: "something went wrong"}
	got = formatInferenceError(err)
	if got.Error() != "something went wrong" {
		t.Fatalf("unexpected: %s", got)
	}

	// Title only
	err = &fantasy.Error{Title: "Connection Failed"}
	got = formatInferenceError(err)
	if got.Error() != "Connection Failed" {
		t.Fatalf("unexpected: %s", got)
	}

	// Empty title and message — falls through to generic handler
	err = &fantasy.Error{}
	got = formatInferenceError(err)
	if got.Error() != "inference failed: " {
		t.Fatalf("unexpected: %s", got)
	}
}

func TestFormatInferenceError_GenericError(t *testing.T) {
	err := formatInferenceError(io.EOF)
	if err.Error() != "inference failed: EOF" {
		t.Fatalf("unexpected: %s", err)
	}
}

func TestFormatInferenceError_WrappedProviderError(t *testing.T) {
	inner := &fantasy.ProviderError{
		Title:      "Bad Gateway",
		Message:    "upstream timeout",
		StatusCode: http.StatusBadGateway,
	}
	wrapped := fmt.Errorf("stream step: %w", inner)

	got := formatInferenceError(wrapped)
	if got.Error() != "Bad Gateway: upstream timeout" {
		t.Fatalf("unexpected: %s", got)
	}
}

func TestInferenceErrorTitle(t *testing.T) {
	tests := []struct {
		pe     *fantasy.ProviderError
		expect string
	}{
		{&fantasy.ProviderError{Title: "Custom"}, "Custom"},
		{&fantasy.ProviderError{StatusCode: 429}, "Rate limited"},
		{&fantasy.ProviderError{StatusCode: 401}, "Authentication failed"},
		{&fantasy.ProviderError{StatusCode: 503}, "Service unavailable"},
		{&fantasy.ProviderError{StatusCode: 418}, "HTTP 418"},
		{&fantasy.ProviderError{}, "Provider error"},
	}
	for _, tt := range tests {
		got := inferenceErrorTitle(tt.pe)
		if got != tt.expect {
			t.Errorf("inferenceErrorTitle(%+v) = %q, want %q", tt.pe, got, tt.expect)
		}
	}
}

func TestUnwrapProviderError(t *testing.T) {
	pe := &fantasy.ProviderError{StatusCode: 429, Message: "rate limited"}

	// Direct
	if got := unwrapProviderError(pe); got != pe {
		t.Fatal("should unwrap direct ProviderError")
	}

	// Wrapped
	wrapped := fmt.Errorf("outer: %w", pe)
	if got := unwrapProviderError(wrapped); got != pe {
		t.Fatal("should unwrap through fmt.Errorf")
	}

	// RetryError wrapping ProviderError
	retryErr := &fantasy.RetryError{Errors: []error{pe}}
	if got := unwrapProviderError(retryErr); got != pe {
		t.Fatal("should unwrap through RetryError")
	}

	// No ProviderError
	if got := unwrapProviderError(errors.New("plain")); got != nil {
		t.Fatal("should return nil for non-ProviderError")
	}
}
