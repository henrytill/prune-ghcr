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

// DefaultBaseDelay is multiplied by the attempt number for each backoff. It is
// a var so that tests of the packages that build their own Options can turn the
// backoff off rather than sleep through it.
var DefaultBaseDelay = 2 * time.Second

// NonRetryableError is an error that retrying cannot fix, such as a 403 from
// the API.
type NonRetryableError struct {
	Message string
}

func (e *NonRetryableError) Error() string { return e.Message }

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

// Options configures a call to Do. Build it with New so the defaults apply; a
// zero BaseDelay means no delay, which is what the tests want.
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

// New returns Options with the default attempt count and backoff.
func New(what string, warn func(string)) Options {
	return Options{
		What:      what,
		Attempts:  DefaultAttempts,
		BaseDelay: DefaultBaseDelay,
		Warn:      warn,
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

		timer := time.NewTimer(time.Duration(attempt) * opts.BaseDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}
