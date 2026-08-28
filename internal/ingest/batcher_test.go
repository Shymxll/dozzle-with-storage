package ingest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

type fakeCopier struct {
	mu        sync.Mutex
	batches   [][]storage.LogRow
	failCount int
}

func (f *fakeCopier) CopyRows(_ context.Context, rows []storage.LogRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCount > 0 {
		f.failCount--
		return errors.New("database unavailable")
	}
	copyRows := append([]storage.LogRow(nil), rows...)
	f.batches = append(f.batches, copyRows)
	return nil
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestBatcherFlushesAtBatchSize(t *testing.T) {
	copier := &fakeCopier{}
	committed := make(chan int, 1)
	b := NewBatcher(copier, 10, 3, time.Hour, func(rows []storage.LogRow) { committed <- len(rows) }, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)

	if err := b.Enqueue([]storage.LogRow{{Message: "1"}, {Message: "2"}, {Message: "3"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case count := <-committed:
		if count != 3 {
			t.Fatalf("committed %d rows", count)
		}
	case <-time.After(time.Second):
		t.Fatal("batch did not flush")
	}
}

func TestBatcherReturnsBackpressure(t *testing.T) {
	b := NewBatcher(&fakeCopier{}, 2, 1000, time.Hour, nil, testLogger())
	if err := b.Enqueue([]storage.LogRow{{}, {}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Enqueue([]storage.LogRow{{}}); !errors.Is(err, ErrBackpressure) {
		t.Fatalf("expected ErrBackpressure, got %v", err)
	}
}

func TestBatcherRetriesWithoutDropping(t *testing.T) {
	copier := &fakeCopier{failCount: 1}
	committed := make(chan struct{}, 1)
	b := NewBatcher(copier, 10, 1, time.Hour, func([]storage.LogRow) { committed <- struct{}{} }, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.Run(ctx)
	if err := b.Enqueue([]storage.LogRow{{Message: "kept"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-committed:
		if b.Pending() != 0 {
			t.Fatalf("pending = %d", b.Pending())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("batch was not retried")
	}
}
