package ingest

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

var ErrBackpressure = errors.New("ingest queue is full")

type Copier interface {
	CopyRows(context.Context, []storage.LogRow) error
}

type Batcher struct {
	copier        Copier
	queue         chan storage.LogRow
	maxPending    int64
	batchSize     int
	flushInterval time.Duration
	afterCommit   func([]storage.LogRow)
	logger        *slog.Logger
	pending       atomic.Int64
}

func NewBatcher(
	copier Copier,
	maxPending int,
	batchSize int,
	flushInterval time.Duration,
	afterCommit func([]storage.LogRow),
	logger *slog.Logger,
) *Batcher {
	return &Batcher{
		copier:        copier,
		queue:         make(chan storage.LogRow, maxPending),
		maxPending:    int64(maxPending),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		afterCommit:   afterCommit,
		logger:        logger,
	}
}

func (b *Batcher) Enqueue(rows []storage.LogRow) error {
	if len(rows) == 0 {
		return nil
	}
	n := int64(len(rows))
	for {
		current := b.pending.Load()
		if n > b.maxPending-current {
			return ErrBackpressure
		}
		if b.pending.CompareAndSwap(current, current+n) {
			break
		}
	}
	for _, row := range rows {
		b.queue <- row
	}
	return nil
}

func (b *Batcher) Pending() int64 {
	return b.pending.Load()
}

func (b *Batcher) Run(ctx context.Context) {
	for {
		var first storage.LogRow
		select {
		case <-ctx.Done():
			b.flushOnShutdown()
			return
		case first = <-b.queue:
		}

		batch := make([]storage.LogRow, 0, b.batchSize)
		batch = append(batch, first)
		timer := time.NewTimer(b.flushInterval)

	collect:
		for len(batch) < b.batchSize {
			select {
			case row := <-b.queue:
				batch = append(batch, row)
			case <-timer.C:
				break collect
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				b.flushOnShutdownWith(batch)
				return
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		if !b.flushWithRetry(ctx, batch) {
			b.flushOnShutdownWith(batch)
			return
		}
	}
}

func (b *Batcher) flushWithRetry(ctx context.Context, batch []storage.LogRow) bool {
	backoff := 250 * time.Millisecond
	for {
		if err := b.copier.CopyRows(ctx, batch); err == nil {
			b.pending.Add(-int64(len(batch)))
			if b.afterCommit != nil {
				b.afterCommit(batch)
			}
			return true
		} else if ctx.Err() == nil {
			b.logger.Error("log batch COPY failed; retaining batch for retry", "rows", len(batch), "error", err, "retry_in", backoff)
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (b *Batcher) flushOnShutdown() {
	b.flushOnShutdownWith(nil)
}

func (b *Batcher) flushOnShutdownWith(batch []storage.LogRow) {
	for {
		select {
		case row := <-b.queue:
			batch = append(batch, row)
		default:
			goto drained
		}
	}

drained:
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for len(batch) > 0 {
		count := min(len(batch), b.batchSize)
		if !b.flushWithRetry(shutdownCtx, batch[:count]) {
			b.logger.Error("shutdown ended with unflushed in-memory logs", "rows", len(batch))
			return
		}
		batch = batch[count:]
	}
}
