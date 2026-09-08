package api

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
		got, err := ParseDuration(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseDuration(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "0d", "-1h", "1x", "d"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) accepted", bad)
		}
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		7 * 24 * time.Hour: "1w",
		48 * time.Hour:     "2d",
		90 * time.Minute:   "1h30m0s",
	} {
		if got := HumanDuration(d); got != want {
			t.Errorf("HumanDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
