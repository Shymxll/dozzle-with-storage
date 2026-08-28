package storage

import (
	"testing"
	"time"
)

func TestPartitionNameUsesUTCMonth(t *testing.T) {
	zone := time.FixedZone("UTC+3", 3*60*60)
	value := time.Date(2026, time.April, 1, 1, 30, 0, 0, zone)
	if got := partitionName(value); got != "logs_2026_03" {
		t.Fatalf("partitionName() = %q", got)
	}
}

func TestFirstOfMonth(t *testing.T) {
	value := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	if got := firstOfMonth(value); !got.Equal(want) {
		t.Fatalf("firstOfMonth() = %s, want %s", got, want)
	}
}
