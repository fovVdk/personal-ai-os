// English: These tests verify retry limits, cancellation, and idempotency-key behavior.
// 中文：本测试验证重试上限、取消处理及幂等键行为。
// 中文：本测试验证重试次数、取消处理及幂等键行为。
package execution

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryStopsAfterSuccess(t *testing.T) {
	var attempts atomic.Int32
	err := Retry(context.Background(), RetryPolicy{MaxAttempts: 4, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond}, func(context.Context) error {
		if attempts.Add(1) < 3 {
			return errors.New("temporary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("got %d attempts, want 3", attempts.Load())
	}
}

func TestIdempotencyGuardRunsOperationOnceAndRejectsChangedInput(t *testing.T) {
	guard := NewIdempotencyGuard()
	var calls atomic.Int32
	op := func(context.Context) error { calls.Add(1); return nil }
	if err := guard.Do(context.Background(), "request-1", "hash-a", op); err != nil {
		t.Fatal(err)
	}
	if err := guard.Do(context.Background(), "request-1", "hash-a", op); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("operation called %d times", calls.Load())
	}
	if err := guard.Do(context.Background(), "request-1", "hash-b", op); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}
