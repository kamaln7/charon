package main

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"15m", 15 * time.Minute},
		{"1d", 24 * time.Hour},
		{"2w", 14 * 24 * time.Hour},
	} {
		got, err := parseDuration(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("parseDuration(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "0d", "-1h", "1x", "d"} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%q) accepted", bad)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		7 * 24 * time.Hour: "1w",
		48 * time.Hour:     "2d",
		90 * time.Minute:   "1h30m0s",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
