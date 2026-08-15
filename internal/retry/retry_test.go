package retry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// testOptions retries without sleeping.
func testOptions(warn func(string)) Options {
	return Options{What: "thing", Attempts: DefaultAttempts, BaseDelay: 0, Warn: warn}
}

func TestReturnsTheResultWithoutRetryingOnSuccess(t *testing.T) {
	calls := 0
	warned := 0

	result, err := Do(context.Background(), func(context.Context) (string, error) {
		calls++
		return "ok", nil
	}, testOptions(func(string) { warned++ }))

	if err != nil || result != "ok" {
		t.Fatalf("Do = %q, %v, want ok, nil", result, err)
	}
	if calls != 1 {
		t.Errorf("called %d times, want 1", calls)
	}
	if warned != 0 {
		t.Errorf("warned %d times, want 0", warned)
	}
}

func TestRetriesATransientFailureAndWarns(t *testing.T) {
	calls := 0
	var warnings []string

	result, err := Do(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("ECONNRESET")
		}
		return "ok", nil
	}, testOptions(func(message string) { warnings = append(warnings, message) }))

	if err != nil || result != "ok" {
		t.Fatalf("Do = %q, %v, want ok, nil", result, err)
	}
	if calls != 2 {
		t.Errorf("called %d times, want 2", calls)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "thing failed (ECONNRESET)") {
		t.Errorf("warnings = %v, want one mentioning thing failed (ECONNRESET)", warnings)
	}
}

func TestDoesNotRetryAnErrorRetryingCannotFix(t *testing.T) {
	calls := 0

	_, err := Do(context.Background(), func(context.Context) (string, error) {
		calls++
		return "", &NonRetryableError{Message: "403 Forbidden"}
	}, testOptions(nil))

	if err == nil || err.Error() != "403 Forbidden" {
		t.Fatalf("error = %v, want 403 Forbidden", err)
	}
	if calls != 1 {
		t.Errorf("called %d times, want 1", calls)
	}
}

func TestDoesNotRetryAWrappedNonRetryableError(t *testing.T) {
	calls := 0

	_, err := Do(context.Background(), func(context.Context) (string, error) {
		calls++
		return "", errors.Join(errors.New("context"), &NonRetryableError{Message: "404"})
	}, testOptions(nil))

	if err == nil {
		t.Fatal("error = nil, want a failure")
	}
	if calls != 1 {
		t.Errorf("called %d times, want 1: errors.As must see through the wrapping", calls)
	}
}

func TestWaitsOutADelayedErrorsOwnDelay(t *testing.T) {
	calls := 0
	start := time.Now()

	result, err := Do(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", &DelayedError{Message: "rate limited", Delay: 50 * time.Millisecond}
		}
		return "ok", nil
	}, testOptions(nil))

	if err != nil || result != "ok" {
		t.Fatalf("Do = %q, %v, want ok, nil", result, err)
	}
	if calls != 2 {
		t.Errorf("called %d times, want 2", calls)
	}
	// testOptions has no backoff of its own, so any wait came from the error.
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("retried after %v, want at least the error's own 50ms delay", elapsed)
	}
}

func TestTreatsTransientStatusesAsRetryableAndTheRestAsPermanent(t *testing.T) {
	for _, status := range []int{500, 502, 429, 408} {
		if !IsRetryableStatus(status) {
			t.Errorf("IsRetryableStatus(%d) = false, want true", status)
		}
	}
	for _, status := range []int{403, 404, 400} {
		if IsRetryableStatus(status) {
			t.Errorf("IsRetryableStatus(%d) = true, want false", status)
		}
	}

	var nonRetryable *NonRetryableError
	if errors.As(StatusError("m", 502), &nonRetryable) {
		t.Error("StatusError(502) is non-retryable, want retryable")
	}
	if !errors.As(StatusError("m", 404), &nonRetryable) {
		t.Error("StatusError(404) is retryable, want non-retryable")
	}
}

func TestRethrowsAfterTheLastAttempt(t *testing.T) {
	calls := 0

	options := testOptions(nil)
	options.Attempts = 2
	_, err := Do(context.Background(), func(context.Context) (string, error) {
		calls++
		return "", errors.New("down")
	}, options)

	if err == nil || err.Error() != "down" {
		t.Fatalf("error = %v, want down", err)
	}
	if calls != 2 {
		t.Errorf("called %d times, want 2", calls)
	}
}

func TestStopsWhenTheContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	options := New("thing", nil)
	calls := 0

	_, err := Do(ctx, func(context.Context) (string, error) {
		calls++
		cancel()
		return "", errors.New("down")
	}, options)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Errorf("called %d times, want 1: the backoff must not outlive the context", calls)
	}
}
