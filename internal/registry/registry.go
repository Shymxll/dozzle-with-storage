package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

const startedSubscriberBuffer = 64

type ServiceSource interface {
	ListServices(context.Context) ([]storage.Service, error)
}

type Service struct {
	ID       string
	Name     string
	Earliest time.Time
}

type Registry struct {
	source  ServiceSource
	logger  *slog.Logger
	refresh time.Duration

	mu          sync.RWMutex
	byID        map[string]Service
	byName      map[string]string
	nextSubID   uint64
	startedSubs map[uint64]chan Service
}

func New(source ServiceSource, refresh time.Duration, logger *slog.Logger) *Registry {
	return &Registry{
		source:      source,
		logger:      logger,
		refresh:     refresh,
		byID:        make(map[string]Service),
		byName:      make(map[string]string),
		startedSubs: make(map[uint64]chan Service),
	}
}

func StableID(service string) string {
	sum := sha256.Sum256([]byte(service))
	return hex.EncodeToString(sum[:])[:12]
}

func (r *Registry) Run(ctx context.Context) {
	ticker := time.NewTicker(r.refresh)
	defer ticker.Stop()
	for {
		if err := r.Refresh(ctx); err != nil && ctx.Err() == nil {
			r.logger.Error("service cache refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *Registry) Refresh(ctx context.Context) error {
	services, err := r.source.ListServices(ctx)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	nextByID := make(map[string]Service, len(services))
	nextByName := make(map[string]string, len(services))
	for _, item := range services {
		service := Service{ID: StableID(item.Name), Name: item.Name, Earliest: item.Earliest.UTC()}
		nextByID[service.ID] = service
		nextByName[service.Name] = service.ID
		if _, existed := r.byID[service.ID]; !existed {
			r.notifyStartedLocked(service)
		}
	}
	r.byID = nextByID
	r.byName = nextByName
	return nil
}

func (r *Registry) Observe(rows []storage.LogRow) {
	earliest := make(map[string]time.Time)
	for _, row := range rows {
		if current, ok := earliest[row.Service]; !ok || row.Timestamp.Before(current) {
			earliest[row.Service] = row.Timestamp.UTC()
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for name, timestamp := range earliest {
		if id, ok := r.byName[name]; ok {
			service := r.byID[id]
			if timestamp.Before(service.Earliest) {
				service.Earliest = timestamp
				r.byID[id] = service
			}
			continue
		}
		service := Service{ID: StableID(name), Name: name, Earliest: timestamp}
		r.byID[service.ID] = service
		r.byName[name] = service.ID
		r.notifyStartedLocked(service)
	}
}

func (r *Registry) Snapshot() []Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Service, 0, len(r.byID))
	for _, service := range r.byID {
		result = append(result, service)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (r *Registry) Lookup(id string) (Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	service, ok := r.byID[id]
	return service, ok
}

func (r *Registry) SubscribeStarted(ctx context.Context) <-chan Service {
	r.mu.Lock()
	r.nextSubID++
	id := r.nextSubID
	ch := make(chan Service, startedSubscriberBuffer)
	r.startedSubs[id] = ch
	r.mu.Unlock()

	go func() {
		<-ctx.Done()
		r.mu.Lock()
		if existing, ok := r.startedSubs[id]; ok {
			delete(r.startedSubs, id)
			close(existing)
		}
		r.mu.Unlock()
	}()
	return ch
}

func (r *Registry) notifyStartedLocked(service Service) {
	for id, ch := range r.startedSubs {
		select {
		case ch <- service:
		default:
			delete(r.startedSubs, id)
			close(ch)
			r.logger.Warn("disconnected slow container-started subscriber")
		}
	}
}
