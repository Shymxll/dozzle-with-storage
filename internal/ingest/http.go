package ingest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

const (
	maxRequestBytes = 32 << 20
	maxLineBytes    = 2 << 20
)

type Queue interface {
	Enqueue([]storage.LogRow) error
	Pending() int64
}

type Pinger interface {
	Ping(context.Context) error
}

type HTTPHandler struct {
	token  string
	queue  Queue
	pinger Pinger
}

func NewHTTPHandler(token string, queue Queue, pinger Pinger) http.Handler {
	h := &HTTPHandler{token: token, queue: queue, pinger: pinger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ingest", h.ingest)
	mux.HandleFunc("GET /healthz", h.health)
	return mux
}

type ingestLine struct {
	Service   *string `json:"svc"`
	Timestamp *string `json:"ts"`
	Message   *string `json:"msg"`
}

func (h *HTTPHandler) ingest(w http.ResponseWriter, r *http.Request) {
	if !constantTimeEqual(r.Header.Get("Authorization"), "Bearer "+h.token) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-ndjson" {
		http.Error(w, "Content-Type must be application/x-ndjson", http.StatusUnsupportedMediaType)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	rows := make([]storage.LogRow, 0, 1000)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var input ingestLine
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			http.Error(w, fmt.Sprintf("invalid NDJSON line %d: %v", lineNumber, err), http.StatusBadRequest)
			return
		}
		if input.Service == nil || strings.TrimSpace(*input.Service) == "" || input.Timestamp == nil || input.Message == nil {
			http.Error(w, fmt.Sprintf("invalid NDJSON line %d: svc, ts and msg are required", lineNumber), http.StatusBadRequest)
			return
		}
		timestamp, err := time.Parse(time.RFC3339Nano, *input.Timestamp)
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid NDJSON line %d timestamp: %v", lineNumber, err), http.StatusBadRequest)
			return
		}
		rows = append(rows, storage.LogRow{
			Service:   strings.TrimSpace(*input.Service),
			Timestamp: timestamp.UTC(),
			Message:   *input.Message,
		})
	}
	if err := scanner.Err(); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.queue.Enqueue(rows); err != nil {
		if errors.Is(err, ErrBackpressure) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "ingest queue is full", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "could not enqueue logs", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": len(rows), "pending": h.queue.Pending()})
}

func (h *HTTPHandler) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.pinger.Ping(ctx); err != nil {
		http.Error(w, "postgres unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
