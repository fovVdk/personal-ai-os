// Package event defines event contracts and the lifecycle for persisted Outbox dispatch.
// 中文：event 包定义领域事件契约和持久 Outbox 发布生命周期。
package event

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrNoOutboxEvents = errors.New("no pending outbox events")

type OutboxRepository interface {
	DispatchBatch(context.Context, string, int, time.Duration, int) (int, error)
}

// Dispatcher repeatedly claims a bounded batch. Durable lease/retry semantics live in PostgreSQL.
// 中文：Dispatcher 重复认领有界批次；持久租约和重试由 PostgreSQL 管理。
type Dispatcher struct {
	Repository    OutboxRepository
	WorkerID      string
	Concurrency   int
	BatchSize     int
	LeaseDuration time.Duration
	PollInterval  time.Duration
	MaxAttempts   int
	OnError       func(error)
}

func (d *Dispatcher) validate() error {
	if d.Repository == nil || strings.TrimSpace(d.WorkerID) == "" || d.Concurrency < 1 || d.BatchSize < 1 || d.BatchSize > 500 || d.LeaseDuration < time.Second || d.PollInterval <= 0 || d.MaxAttempts < 1 {
		return errors.New("Outbox dispatcher configuration is invalid")
	}
	return nil
}

func (d *Dispatcher) Run(ctx context.Context) error {
	if err := d.validate(); err != nil {
		return err
	}
	var group sync.WaitGroup
	for index := 0; index < d.Concurrency; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			d.runLoop(ctx, index)
		}(index)
	}
	<-ctx.Done()
	group.Wait()
	return nil
}

func (d *Dispatcher) runLoop(ctx context.Context, index int) {
	workerID := fmt.Sprintf("%s-%d", d.WorkerID, index)
	ticker := time.NewTicker(d.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		_, err := d.Repository.DispatchBatch(ctx, workerID, d.BatchSize, d.LeaseDuration, d.MaxAttempts)
		if err != nil && d.OnError != nil {
			d.OnError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
