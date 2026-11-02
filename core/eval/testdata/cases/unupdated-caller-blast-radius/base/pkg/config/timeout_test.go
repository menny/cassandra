package config

import "testing"

func TestParseTimeout(t *testing.T) {
	got, err := ParseTimeout("5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 5 {
		t.Fatalf("expected 5 seconds, got %d", got)
	}
}
