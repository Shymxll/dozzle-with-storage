CREATE TABLE IF NOT EXISTS logs (
  svc text NOT NULL,
  ts  timestamptz NOT NULL,
  msg text NOT NULL
) PARTITION BY RANGE (ts);

CREATE INDEX IF NOT EXISTS logs_svc_ts_idx ON logs (svc, ts DESC);

-- Aylik partitionlar uygulama tarafindan otomatik olusturulur. Ornek:
-- CREATE TABLE logs_2026_08 PARTITION OF logs
--   FOR VALUES FROM ('2026-08-01T00:00:00Z') TO ('2026-09-01T00:00:00Z');
