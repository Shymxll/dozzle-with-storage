package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sumartiot/dozzle-log-archive/internal/storage"
)

type fakeQueue struct {
	rows []storage.LogRow
	err  error
}

func (f *fakeQueue) Enqueue(rows []storage.LogRow) error {
	f.rows = append(f.rows, rows...)
	return f.err
}
func (f *fakeQueue) Pending() int64 { return int64(len(f.rows)) }

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestIngestAcceptsValidNDJSON(t *testing.T) {
	queue := &fakeQueue{}
	handler := NewHTTPHandler("secret", queue, fakePinger{})
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("{\"svc\":\"api\",\"ts\":\"2026-08-28T12:34:56.123Z\",\"msg\":\"hello\"}\n"))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/x-ndjson")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if len(queue.rows) != 1 || queue.rows[0].Service != "api" || queue.rows[0].Message != "hello" {
		t.Fatalf("unexpected rows: %#v", queue.rows)
	}
}

func TestIngestRejectsUnauthorizedBeforeParsing(t *testing.T) {
	handler := NewHTTPHandler("secret", &fakeQueue{}, fakePinger{})
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("bad"))
	req.Header.Set("Content-Type", "application/x-ndjson")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.Code)
	}
}

func TestIngestReturns429WithoutAcceptingRows(t *testing.T) {
	queue := &fakeQueue{err: ErrBackpressure}
	handler := NewHTTPHandler("secret", queue, fakePinger{})
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("{\"svc\":\"api\",\"ts\":\"2026-08-28T12:34:56Z\",\"msg\":\"hello\"}\n"))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/x-ndjson")
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests || res.Header().Get("Retry-After") != "2" {
		t.Fatalf("status = %d, retry-after = %q", res.Code, res.Header().Get("Retry-After"))
	}
}

func TestHealthReportsDatabaseFailure(t *testing.T) {
	handler := NewHTTPHandler("secret", &fakeQueue{}, fakePinger{err: errors.New("down")})
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", res.Code)
	}
}
