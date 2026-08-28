package registry

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

type fakeSource struct{ services []storage.Service }

func (f fakeSource) ListServices(context.Context) ([]storage.Service, error) { return f.services, nil }

func TestStableID(t *testing.T) {
	a := StableID("sumart-dashboard-backend")
	b := StableID("sumart-dashboard-backend")
	if a != b || len(a) != 12 {
		t.Fatalf("unstable ID: %q %q", a, b)
	}
}

func TestObserveAddsServiceAndKeepsEarliest(t *testing.T) {
	r := New(fakeSource{}, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := r.SubscribeStarted(ctx)
	now := time.Now().UTC()
	r.Observe([]storage.LogRow{{Service: "svc", Timestamp: now}, {Service: "svc", Timestamp: now.Add(-time.Hour)}})

	service := <-started
	if !service.Earliest.Equal(now.Add(-time.Hour)) {
		t.Fatalf("earliest = %s", service.Earliest)
	}
	if got, ok := r.Lookup(service.ID); !ok || got.Name != "svc" {
		t.Fatalf("lookup failed: %#v %v", got, ok)
	}
}
