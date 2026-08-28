package storage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

// TestMillionRowsQueryLatency is opt-in because it inserts one million rows.
// Run it only against a disposable PostgreSQL database.
func TestMillionRowsQueryLatency(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	store, err := New(ctx, databaseURL, 6, 50_000, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}

	service := fmt.Sprintf("performance-%d", time.Now().UnixNano())
	base := time.Now().UTC().Add(-time.Hour)
	const total = 1_000_000
	const copyBatch = 10_000
	rows := make([]LogRow, copyBatch)
	for offset := 0; offset < total; offset += copyBatch {
		for i := range rows {
			sequence := offset + i
			rows[i] = LogRow{
				Service:   service,
				Timestamp: base.Add(time.Duration(sequence) * time.Microsecond),
				Message:   fmt.Sprintf("performance log %07d", sequence),
			}
		}
		if err := store.CopyRows(ctx, rows); err != nil {
			t.Fatalf("insert batch at %d: %v", offset, err)
		}
	}

	started := time.Now()
	got, err := store.LatestSince(ctx, service, base, base.Add(2*time.Hour))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50_000 {
		t.Fatalf("got %d rows, want 50000", len(got))
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("query took %s, acceptance target is below 500ms", elapsed)
	}
}
