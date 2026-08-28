package live

import (
	"context"
	"log/slog"
	"sync"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

const subscriberBuffer = 1024

type Broker struct {
	logger *slog.Logger

	mu     sync.Mutex
	nextID uint64
	subs   map[string]map[uint64]chan storage.LogRow
}

func NewBroker(logger *slog.Logger) *Broker {
	return &Broker{
		logger: logger,
		subs:   make(map[string]map[uint64]chan storage.LogRow),
	}
}

func (b *Broker) Subscribe(ctx context.Context, service string) <-chan storage.LogRow {
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	ch := make(chan storage.LogRow, subscriberBuffer)
	if b.subs[service] == nil {
		b.subs[service] = make(map[uint64]chan storage.LogRow)
	}
	b.subs[service][id] = ch
	b.mu.Unlock()

	go func() {
		<-ctx.Done()
		b.remove(service, id)
	}()
	return ch
}

func (b *Broker) Publish(rows []storage.LogRow) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, row := range rows {
		for id, ch := range b.subs[row.Service] {
			select {
			case ch <- row:
			default:
				delete(b.subs[row.Service], id)
				close(ch)
				b.logger.Warn("disconnected slow live log subscriber", "service", row.Service)
			}
		}
		if len(b.subs[row.Service]) == 0 {
			delete(b.subs, row.Service)
		}
	}
}

func (b *Broker) remove(service string, id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	serviceSubs := b.subs[service]
	if ch, ok := serviceSubs[id]; ok {
		delete(serviceSubs, id)
		close(ch)
	}
	if len(serviceSubs) == 0 {
		delete(b.subs, service)
	}
}
