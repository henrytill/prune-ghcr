// Package retry provides the linear backoff used for every request the action
// makes, and the error vocabulary that decides what is worth retrying.
package retry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// DefaultAttempts is the total number of attempts, including the first.
const DefaultAttempts = 3

// DefaultBaseDelay is multiplied by the attempt number for each backoff.
const DefaultBaseDelay = 2 * time.Second

// NonRetryableError is an error that retrying cannot fix, such as a 403 from
// the API.
type NonRetryableError struct {
	Message string
}

func (e *NonRetryableError) Error() string { return e.Message }

// DelayedError is a retryable error that knows how long the next attempt
// should wait, such as a rate limit carrying its own reset time. Do waits the
// larger of Delay and the backoff it would have used anyway, so a Delay of
// zero or less changes nothing.
type DelayedError struct {
	Message string
	Delay   time.Duration
}

func (e *DelayedError) Error() string { return e.Message }

// IsRetryableStatus reports whether a request that returned status is worth
// retrying.
func IsRetryableStatus(status int) bool {
	return status == 408 || status == 429 || status >= 500
}

// StatusError builds an error for a failed request, marking it retryable or
// not by status.
func StatusError(message string, status int) error {
	if IsRetryableStatus(status) {
		return errors.New(message)
	}
	return &NonRetryableError{Message: message}
}

// Options configures a call to Do. Build it with Backoff.Options so the
// defaults apply; a zero BaseDelay means no delay, which is what the tests
// want.
type Options struct {
	// What describes the operation, and is used in the retry log line.
	What      string
	Attempts  int
	BaseDelay time.Duration
	// Warn receives one line per retry. It is a function rather than a
	// dependency on the actions package so that retry stays testable without
	// capturing stdout.
	Warn func(string)
}

// Backoff is the operation-independent half of Options: the warn sink and
// backoff unit a client carries between calls. It exists so each client does
// not grow its own copy of this seam; tests zero BaseDelay through it.
type Backoff struct {
	BaseDelay time.Duration
	Warn      func(string)
}

// NewBackoff returns a Backoff with the default delay.
func NewBackoff(warn func(string)) Backoff {
	return Backoff{BaseDelay: DefaultBaseDelay, Warn: warn}
}

// Options builds the retry configuration for one operation.
func (b Backoff) Options(what string) Options {
	return Options{
		What:      what,
		Attempts:  DefaultAttempts,
		BaseDelay: b.BaseDelay,
		Warn:      b.Warn,
	}
}

// Do runs fn, retrying on any failure with a linear backoff.
//
// Transient network and TLS failures against the API and the registry are
// common enough to be worth retrying rather than failing the workflow. A
// NonRetryableError stops immediately: retrying a 403 or a 404 only burns time.
func Do[T any](ctx context.Context, fn func(context.Context) (T, error), opts Options) (T, error) {
	attempts := opts.Attempts
	if attempts < 1 {
		attempts = DefaultAttempts
	}

	var zero T
	for attempt := 1; ; attempt++ {
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}

		var nonRetryable *NonRetryableError
		if attempt >= attempts || errors.As(err, &nonRetryable) {
			return zero, err
		}

		if opts.Warn != nil {
			opts.Warn(fmt.Sprintf("%s failed (%s); retry %d/%d",
				opts.What, err, attempt, attempts-1))
		}

		delay := time.Duration(attempt) * opts.BaseDelay
		var delayed *DelayedError
		if errors.As(err, &delayed) && delayed.Delay > delay {
			delay = delayed.Delay
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}
