package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maintenanceInterval = 6 * time.Hour

type LogRow struct {
	Service   string
	Timestamp time.Time
	Message   string
}

type Service struct {
	Name     string
	Earliest time.Time
}

type Store struct {
	pool            *pgxpool.Pool
	retentionMonths int
	maxRows         int
	logger          *slog.Logger

	schemaMu    sync.Mutex
	schemaReady bool
}

func New(ctx context.Context, databaseURL string, retentionMonths, maxRows int, logger *slog.Logger) (*Store, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "dozzle-log-archive"

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	return &Store{
		pool:            pool,
		retentionMonths: retentionMonths,
		maxRows:         maxRows,
		logger:          logger,
	}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if s.schemaReady {
		return nil
	}

	const table = `
CREATE TABLE IF NOT EXISTS logs (
  svc text NOT NULL,
  ts  timestamptz NOT NULL,
  msg text NOT NULL
) PARTITION BY RANGE (ts);`
	if _, err := s.pool.Exec(ctx, table); err != nil {
		return fmt.Errorf("ensure logs schema: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `CREATE INDEX IF NOT EXISTS logs_svc_ts_idx ON logs (svc, ts DESC)`); err != nil {
		return fmt.Errorf("ensure logs index: %w", err)
	}
	s.schemaReady = true
	return nil
}

func (s *Store) EnsurePartition(ctx context.Context, timestamp time.Time) error {
	if err := s.EnsureSchema(ctx); err != nil {
		return err
	}
	start := firstOfMonth(timestamp)
	end := start.AddDate(0, 1, 0)
	name := partitionName(start)
	query := fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s PARTITION OF logs FOR VALUES FROM ('%s') TO ('%s')",
		pgx.Identifier{name}.Sanitize(),
		start.Format(time.RFC3339),
		end.Format(time.RFC3339),
	)
	if _, err := s.pool.Exec(ctx, query); err != nil {
		return fmt.Errorf("ensure partition %s: %w", name, err)
	}
	return nil
}

func (s *Store) CopyRows(ctx context.Context, rows []LogRow) error {
	if len(rows) == 0 {
		return nil
	}

	months := make(map[time.Time]struct{})
	for _, row := range rows {
		months[firstOfMonth(row.Timestamp)] = struct{}{}
	}
	for month := range months {
		if err := s.EnsurePartition(ctx, month); err != nil {
			return err
		}
	}

	count, err := s.pool.CopyFrom(
		ctx,
		pgx.Identifier{"logs"},
		[]string{"svc", "ts", "msg"},
		pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			return []any{rows[i].Service, rows[i].Timestamp, rows[i].Message}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("COPY logs: %w", err)
	}
	if count != int64(len(rows)) {
		return fmt.Errorf("COPY logs wrote %d of %d rows", count, len(rows))
	}
	return nil
}

func (s *Store) ListServices(ctx context.Context) ([]Service, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT svc, MIN(ts) FROM logs GROUP BY svc ORDER BY svc`)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()

	services := make([]Service, 0)
	for rows.Next() {
		var service Service
		if err := rows.Scan(&service.Name, &service.Earliest); err != nil {
			return nil, fmt.Errorf("scan service: %w", err)
		}
		service.Earliest = service.Earliest.UTC()
		services = append(services, service)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate services: %w", err)
	}
	return services, nil
}

func (s *Store) LogsBetween(ctx context.Context, service string, since, until time.Time) ([]LogRow, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
SELECT svc, ts, msg
FROM logs
WHERE svc = $1 AND ts >= $2 AND ts <= $3
ORDER BY ts
LIMIT $4`, service, since, until, s.maxRows)
	if err != nil {
		return nil, fmt.Errorf("query logs between dates: %w", err)
	}
	return collectRows(rows)
}

func (s *Store) LatestSince(ctx context.Context, service string, since, until time.Time) ([]LogRow, error) {
	if err := s.EnsureSchema(ctx); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
SELECT svc, ts, msg
FROM (
  SELECT svc, ts, msg
  FROM logs
  WHERE svc = $1 AND ts >= $2 AND ts <= $3
  ORDER BY ts DESC
  LIMIT $4
) AS latest
ORDER BY ts`, service, since, until, s.maxRows)
	if err != nil {
		return nil, fmt.Errorf("query latest logs: %w", err)
	}
	return collectRows(rows)
}

func collectRows(rows pgx.Rows) ([]LogRow, error) {
	defer rows.Close()
	result := make([]LogRow, 0)
	for rows.Next() {
		var row LogRow
		if err := rows.Scan(&row.Service, &row.Timestamp, &row.Message); err != nil {
			return nil, fmt.Errorf("scan log row: %w", err)
		}
		row.Timestamp = row.Timestamp.UTC()
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate log rows: %w", err)
	}
	return result, nil
}

func (s *Store) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()

	for {
		if err := s.Maintain(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			s.logger.Error("partition maintenance failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Store) Maintain(ctx context.Context, now time.Time) error {
	if err := s.EnsurePartition(ctx, now); err != nil {
		return err
	}
	if err := s.EnsurePartition(ctx, firstOfMonth(now).AddDate(0, 1, 0)); err != nil {
		return err
	}
	return s.dropExpiredPartitions(ctx, now)
}

func (s *Store) dropExpiredPartitions(ctx context.Context, now time.Time) error {
	oldestKept := firstOfMonth(now).AddDate(0, 1-s.retentionMonths, 0)
	rows, err := s.pool.Query(ctx, `
SELECT child_ns.nspname, child.relname
FROM pg_inherits
JOIN pg_class parent ON parent.oid = inhparent
JOIN pg_class child ON child.oid = inhrelid
JOIN pg_namespace child_ns ON child_ns.oid = child.relnamespace
WHERE parent.oid = 'logs'::regclass
  AND child.relname ~ '^logs_[0-9]{4}_[0-9]{2}$'`)
	if err != nil {
		return fmt.Errorf("list log partitions: %w", err)
	}

	type partition struct{ schema, name string }
	var expired []partition
	for rows.Next() {
		var p partition
		if err := rows.Scan(&p.schema, &p.name); err != nil {
			rows.Close()
			return fmt.Errorf("scan partition: %w", err)
		}
		month, err := time.Parse("2006_01", strings.TrimPrefix(p.name, "logs_"))
		if err == nil && month.Before(oldestKept) {
			expired = append(expired, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate partitions: %w", err)
	}
	rows.Close()

	for _, p := range expired {
		name := pgx.Identifier{p.schema, p.name}.Sanitize()
		if _, err := s.pool.Exec(ctx, "DROP TABLE IF EXISTS "+name); err != nil {
			return fmt.Errorf("drop partition %s: %w", p.name, err)
		}
		s.logger.Info("dropped expired log partition", "partition", p.name)
	}
	return nil
}

func firstOfMonth(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func partitionName(value time.Time) string {
	return "logs_" + firstOfMonth(value).Format("2006_01")
}
