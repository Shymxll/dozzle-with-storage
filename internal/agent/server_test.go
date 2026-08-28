package agent

import (
	"context"
	"testing"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/agent/pb"
	"github.com/sumartiot/dozzle-log-archive/internal/registry"
	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

type fakeStore struct{}

func (fakeStore) LogsBetween(context.Context, string, time.Time, time.Time) ([]storage.LogRow, error) {
	return nil, nil
}
func (fakeStore) LatestSince(context.Context, string, time.Time, time.Time) ([]storage.LogRow, error) {
	return nil, nil
}

type fakeRegistry struct{ services []registry.Service }

func (f fakeRegistry) Snapshot() []registry.Service { return f.services }
func (f fakeRegistry) Lookup(id string) (registry.Service, bool) {
	for _, service := range f.services {
		if service.ID == id {
			return service, true
		}
	}
	return registry.Service{}, false
}
func (fakeRegistry) SubscribeStarted(context.Context) <-chan registry.Service {
	return make(chan registry.Service)
}

type fakeBroker struct{}

func (fakeBroker) Subscribe(context.Context, string) <-chan storage.LogRow {
	return make(chan storage.LogRow)
}

func TestListContainersUsesStableArchiveFields(t *testing.T) {
	earliest := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	service := registry.Service{ID: registry.StableID("api"), Name: "api", Earliest: earliest}
	server := NewService(fakeStore{}, fakeRegistry{services: []registry.Service{service}}, fakeBroker{})
	response, err := server.ListContainers(context.Background(), &pb.ListContainersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Containers) != 1 {
		t.Fatalf("containers = %d", len(response.Containers))
	}
	container := response.Containers[0]
	if container.Id != service.ID || container.Name != "api" || container.State != "running" || container.Host != ArchiveHostID {
		t.Fatalf("unexpected container: %#v", container)
	}
	if !container.Created.AsTime().Equal(earliest) || !container.Started.AsTime().Equal(earliest) {
		t.Fatalf("unexpected timestamps: %s %s", container.Created.AsTime(), container.Started.AsTime())
	}
}

func TestLogEventMatchesDozzleMillisecondWireConvention(t *testing.T) {
	timestamp := time.Date(2026, 8, 28, 12, 34, 56, 123_000_000, time.UTC)
	event, err := logEvent("abc", storage.LogRow{Timestamp: timestamp, Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if event.Timestamp.Seconds != timestamp.UnixMilli() || event.Timestamp.Nanos != 0 {
		t.Fatalf("wire timestamp = %#v, want seconds=%d", event.Timestamp, timestamp.UnixMilli())
	}
	var message pb.SingleMessage
	if err := event.Message.UnmarshalTo(&message); err != nil {
		t.Fatal(err)
	}
	if message.Message != "hello" || event.Level != "unknown" || event.Stream != "stdout" || event.Type != "single" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestMutatingRPCsFailReadOnly(t *testing.T) {
	server := NewService(fakeStore{}, fakeRegistry{}, fakeBroker{})
	if _, err := server.ContainerAction(context.Background(), &pb.ContainerActionRequest{}); err == nil {
		t.Fatal("expected read-only error")
	}
}
