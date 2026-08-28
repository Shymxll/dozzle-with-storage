package live

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

func TestBrokerRoutesByService(t *testing.T) {
	broker := NewBroker(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := broker.Subscribe(ctx, "a")
	b := broker.Subscribe(ctx, "b")
	broker.Publish([]storage.LogRow{{Service: "a", Message: "hello"}})

	select {
	case row := <-a:
		if row.Message != "hello" {
			t.Fatalf("unexpected row: %#v", row)
		}
	case <-time.After(time.Second):
		t.Fatal("service a did not receive row")
	}
	select {
	case <-b:
		t.Fatal("service b received service a row")
	default:
	}
}
