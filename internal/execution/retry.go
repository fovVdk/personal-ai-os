// Package execution contains bounded, idempotency-aware execution primitives.
// 中文：execution 包提供带重试上限并考虑幂等性的执行基础能力。
// English: This file provides bounded retry and in-process duplicate-call coalescing primitives.
// 中文：本文件提供有界重试和进程内重复调用合并基础能力；它不是持久化幂等账本。
package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var ErrInvalidRetryPolicy = errors.New("invalid retry policy")
var ErrIdempotencyConflict = errors.New("idempotency key already used with different input")

type RetryPolicy struct {
	MaxAttempts  int
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

// Retry runs a bounded operation with exponential backoff and context cancellation.
// 中文：Retry 使用指数退避和 Context 取消执行次数受限的操作。
func Retry(ctx context.Context, policy RetryPolicy, operation func(context.Context) error) error {
	if policy.MaxAttempts < 1 || policy.InitialDelay < 0 || policy.MaxDelay < policy.InitialDelay {
		return ErrInvalidRetryPolicy
	}
	var lastErr error
	delay := policy.InitialDelay
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = operation(ctx)
		if lastErr == nil {
			return nil
		}
		if attempt == policy.MaxAttempts {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
		if delay < policy.MaxDelay {
			delay *= 2
			if delay > policy.MaxDelay {
				delay = policy.MaxDelay
			}
		}
	}
	return fmt.Errorf("operation failed after %d attempts: %w", policy.MaxAttempts, lastErr)
}

type idempotencyEntry struct {
	fingerprint string
	done        chan struct{}
	err         error
}

// IdempotencyGuard coalesces concurrent duplicate operations and rejects key reuse with different input.
// 中文：IdempotencyGuard 合并并发重复操作，并拒绝将同一键用于不同输入；该保证仅限当前进程。
type IdempotencyGuard struct {
	mu      sync.Mutex
	entries map[string]*idempotencyEntry
}

func NewIdempotencyGuard() *IdempotencyGuard {
	return &IdempotencyGuard{entries: make(map[string]*idempotencyEntry)}
}

func (g *IdempotencyGuard) Do(ctx context.Context, key, fingerprint string, operation func(context.Context) error) error {
	if key == "" || fingerprint == "" {
		return errors.New("idempotency key and fingerprint are required")
	}
	g.mu.Lock()
	if existing, ok := g.entries[key]; ok {
		if existing.fingerprint != fingerprint {
			g.mu.Unlock()
			return ErrIdempotencyConflict
		}
		done := existing.done
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return existing.err
		}
	}
	entry := &idempotencyEntry{fingerprint: fingerprint, done: make(chan struct{})}
	g.entries[key] = entry
	g.mu.Unlock()

	entry.err = operation(ctx)
	g.mu.Lock()
	close(entry.done)
	if entry.err != nil {
		delete(g.entries, key)
	}
	g.mu.Unlock()
	return entry.err
}
