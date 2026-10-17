// Package translationerror defines terminal failure categories for translation providers.
package translationerror

import (
	"context"
	"errors"
)

type Kind string

const (
	Unsupported    Kind = "unsupported"
	InvalidOutput  Kind = "invalid_output"
	Authentication Kind = "authentication"
	Connectivity   Kind = "connectivity"
	Timeout        Kind = "timeout"
	Cancellation   Kind = "cancellation"
)

type Error struct {
	Kind  Kind
	Cause error
}

func (e *Error) Error() string { return string(e.Kind) + ": " + e.Cause.Error() }
func (e *Error) Unwrap() error { return e.Cause }

func New(kind Kind, cause error) error {
	if cause == nil {
		return nil
	}
	return &Error{Kind: kind, Cause: cause}
}

func KindOf(err error) Kind {
	var classified *Error
	if errors.As(err, &classified) {
		return classified.Kind
	}
	if errors.Is(err, context.Canceled) {
		return Cancellation
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	return ""
}
