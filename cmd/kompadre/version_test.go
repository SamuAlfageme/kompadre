package main

import (
	"testing"
	"time"
)

func TestFormatBuildTime(t *testing.T) {
	t.Parallel()
	utc := "2026-05-22T08:24:46Z"
	parsed, err := time.Parse(time.RFC3339, utc)
	if err != nil {
		t.Fatal(err)
	}
	want := parsed.Local().Format("2006-01-02 15:04:05 MST")
	got := formatBuildTime(utc)
	if got != want {
		t.Fatalf("formatBuildTime(%q) = %q, want %q", utc, got, want)
	}
	if got == utc {
		t.Fatal("expected local formatting, got raw UTC input")
	}
}

func TestFormatBuildTimeInvalid(t *testing.T) {
	t.Parallel()
	raw := "not-a-timestamp"
	if got := formatBuildTime(raw); got != raw {
		t.Fatalf("formatBuildTime(%q) = %q, want passthrough", raw, got)
	}
}
