package llm

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type availabilityHTTPError struct{ status int }

func (e availabilityHTTPError) Error() string             { return fmt.Sprintf("HTTP %d", e.status) }
func (e availabilityHTTPError) CompletionHTTPStatus() int { return e.status }

type availabilityTimeoutError struct{}

func (availabilityTimeoutError) Error() string   { return "timeout" }
func (availabilityTimeoutError) Timeout() bool   { return true }
func (availabilityTimeoutError) Temporary() bool { return true }

type availabilityTransportError struct{}

func (availabilityTransportError) Error() string             { return "transport error" }
func (availabilityTransportError) CompletionHTTPStatus() int { return 0 }
func (availabilityTransportError) CompletionErrorCode() string {
	return "transport_error"
}

type availabilityResponseReadError struct{}

func (availabilityResponseReadError) Error() string { return "response read error" }
func (availabilityResponseReadError) CompletionErrorCode() string {
	return "response_read_error"
}

func TestIsAvailabilityError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadline", err: fmt.Errorf("wrapped: %w", context.DeadlineExceeded), want: true},
		{name: "network timeout", err: fmt.Errorf("wrapped: %w", availabilityTimeoutError{}), want: true},
		{name: "typed transport error", err: availabilityTransportError{}, want: true},
		{name: "typed response read error", err: availabilityResponseReadError{}, want: true},
		{name: "HTTP 408", err: availabilityHTTPError{status: 408}, want: true},
		{name: "HTTP 429", err: availabilityHTTPError{status: 429}, want: true},
		{name: "HTTP 500", err: availabilityHTTPError{status: 500}, want: true},
		{name: "HTTP 503", err: availabilityHTTPError{status: 503}, want: true},
		{name: "HTTP 400", err: availabilityHTTPError{status: 400}},
		{name: "HTTP 401", err: availabilityHTTPError{status: 401}},
		{name: "HTTP 403", err: availabilityHTTPError{status: 403}},
		{name: "HTTP 422", err: availabilityHTTPError{status: 422}},
		{name: "cancelled", err: context.Canceled},
		{name: "capture", err: fmt.Errorf("wrapped: %w", ErrCompletionCapture)},
		{name: "validation", err: errors.New("strict output validation failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsAvailabilityError(test.err); got != test.want {
				t.Fatalf("IsAvailabilityError(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}
