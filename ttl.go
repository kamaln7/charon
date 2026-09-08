package main

import (
	"fmt"
	"strconv"
	"time"
)

var ttlOptions = []string{"15m", "1h", "6h", "1d", "3d", "1w"}

// TTLOptions is the preset list the UI offers, minus anything the operator's
// ceiling forbids. Offering a choice the server would reject is worse than
// offering fewer.
func (l Limits) TTLOptions() []string {
	var out []string
	for _, o := range ttlOptions {
		if d, err := parseDuration(o); err == nil && d <= l.MaxTTL {
			out = append(out, o)
		}
	}
	return out
}

// DefaultTTLOption returns the preset label matching the configured default so
// the UI can pre-select a button: Go renders 24h as "24h0m0s", which matches
// no option the form offers.
func (l Limits) DefaultTTLOption() string {
	opts := l.TTLOptions()
	for _, o := range opts {
		if d, err := parseDuration(o); err == nil && d == l.DefaultTTL {
			return o
		}
	}
	if len(opts) > 0 {
		return opts[len(opts)-1]
	}
	return ""
}

// parseLinger defaults to zero when omitted. The operator's CHARON_LINGER is
// a ceiling, not a default: agents want burn-on-read, a Mini App wants a
// copy window and must ask for one.
func parseLinger(s string, l Limits) (time.Duration, error) {
	if s == "" || s == "0" || s == "0s" {
		return 0, nil
	}
	d, err := parseDuration(s)
	if err != nil {
		return 0, err
	}
	if d > l.Linger {
		return 0, fmt.Errorf("linger %s exceeds the maximum of %s", s, humanDuration(l.Linger))
	}
	return d, nil
}

// parseNonNegativeDuration accepts 0, unlike parseDuration which is for TTLs.
func parseNonNegativeDuration(s string) (time.Duration, error) {
	if s == "0" || s == "0s" {
		return 0, nil
	}
	return parseDuration(s)
}

// parseTTL defaults when omitted and rejects anything past the ceiling.
func parseTTL(s string, l Limits) (time.Duration, error) {
	if s == "" {
		return min(l.DefaultTTL, l.MaxTTL), nil
	}
	d, err := parseDuration(s)
	if err != nil {
		return 0, err
	}
	if d > l.MaxTTL {
		return 0, fmt.Errorf("ttl %s exceeds the maximum of %s", s, humanDuration(l.MaxTTL))
	}
	return d, nil
}

// parseDuration extends time.ParseDuration with d and w, which it refuses to
// support but every human writing a TTL expects.
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if last := s[len(s)-1]; last == 'd' || last == 'w' {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		unit := 24 * time.Hour
		if last == 'w' {
			unit = 7 * 24 * time.Hour
		}
		return time.Duration(n) * unit, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// humanDuration renders a duration the way it would have been written. Go's
// String() turns a 7-day cap into "168h0m0s", which is a poor thing to put in
// an error someone has to read.
func humanDuration(d time.Duration) string {
	switch {
	case d%(7*24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(7*24*time.Hour)), 10) + "w"
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	default:
		return d.String()
	}
}
