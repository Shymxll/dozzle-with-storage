package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("INGEST_TOKEN", "secret")
	t.Setenv("DOZZLE_CERT", "/cert.pem")
	t.Setenv("DOZZLE_KEY", "/key.pem")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCAddr != ":7007" || cfg.HTTPAddr != ":8080" {
		t.Fatalf("unexpected addresses: %#v", cfg)
	}
	if cfg.RetentionMonths != 6 || cfg.MaxRowsPerQuery != 50_000 || cfg.MaxPendingRows != 100_000 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("INGEST_TOKEN", "secret")
	t.Setenv("DOZZLE_CERT", "/cert.pem")
	t.Setenv("DOZZLE_KEY", "/key.pem")
	t.Setenv("ARCHIVE_RETENTION_MONTHS", "0")

	if _, err := Load(); err == nil {
		t.Fatal("expected invalid retention error")
	}
}
