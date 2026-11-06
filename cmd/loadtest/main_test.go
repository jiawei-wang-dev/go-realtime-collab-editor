package main

import (
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	values := []time.Duration{5 * time.Millisecond, time.Millisecond, 3 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	if got := percentile(values, 0.50); got != 3 {
		t.Fatalf("p50 = %f", got)
	}
	if got := percentile(values, 0.99); got != 4 {
		t.Fatalf("p99 = %f", got)
	}
}

func TestLoadConfigurationRejectsUnsafeValues(t *testing.T) {
	t.Setenv("LOAD_NODES", "127.0.0.1:8443")
	t.Setenv("LOAD_API_URL", "http://127.0.0.1:8080")
	t.Setenv("LOAD_CA_FILE", "/tmp/ca.crt")
	t.Setenv("LOAD_CLIENTS", "0")
	if _, err := loadConfiguration(); err == nil {
		t.Fatal("zero clients were accepted")
	}
}
