package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

const (
	defaultGRPCAddr       = ":7007"
	defaultHTTPAddr       = ":8080"
	defaultRetention      = 6
	defaultMaxRows        = 50_000
	defaultMaxPendingRows = 100_000
)

type Config struct {
	DatabaseURL     string
	IngestToken     string
	DozzleCert      string
	DozzleKey       string
	GRPCAddr        string
	HTTPAddr        string
	RetentionMonths int
	MaxRowsPerQuery int
	MaxPendingRows  int
}

func Load() (Config, error) {
	cfg := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		IngestToken: os.Getenv("INGEST_TOKEN"),
		DozzleCert:  os.Getenv("DOZZLE_CERT"),
		DozzleKey:   os.Getenv("DOZZLE_KEY"),
		GRPCAddr:    valueOrDefault("GRPC_ADDR", defaultGRPCAddr),
		HTTPAddr:    valueOrDefault("HTTP_ADDR", defaultHTTPAddr),
	}

	var err error
	if cfg.RetentionMonths, err = positiveInt("ARCHIVE_RETENTION_MONTHS", defaultRetention); err != nil {
		return Config{}, err
	}
	if cfg.MaxRowsPerQuery, err = positiveInt("MAX_ROWS_PER_QUERY", defaultMaxRows); err != nil {
		return Config{}, err
	}
	if cfg.MaxPendingRows, err = positiveInt("INGEST_MAX_PENDING_ROWS", defaultMaxPendingRows); err != nil {
		return Config{}, err
	}

	missing := make([]string, 0, 4)
	for name, value := range map[string]string{
		"DATABASE_URL": cfg.DatabaseURL,
		"INGEST_TOKEN": cfg.IngestToken,
		"DOZZLE_CERT":  cfg.DozzleCert,
		"DOZZLE_KEY":   cfg.DozzleKey,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment variables: %v", missing)
	}
	if cfg.GRPCAddr == cfg.HTTPAddr {
		return Config{}, errors.New("GRPC_ADDR and HTTP_ADDR must be different")
	}

	return cfg, nil
}

func valueOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func positiveInt(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
