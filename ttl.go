package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/kamaln7/charon/internal/api"
)

var ttlOptions = []string{"15m", "1h", "6h", "1d", "3d", "1w"}

// TTLOptions is the preset list the UI offers, minus anything the operator's
// ceiling forbids. Offering a choice the server would reject is worse than
// offering fewer.
func (l Limits) TTLOptions() []string {
	var out []string
	for _, o := range ttlOptions {
		if d, err := api.ParseDuration(o); err == nil && d <= l.MaxTTL {
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
		if d, err := api.ParseDuration(o); err == nil && d == l.DefaultTTL {
			return o
		}
	}
	if len(opts) > 0 {
		return opts[len(opts)-1]
	}
	return ""
}

// parseTTL defaults when omitted and rejects anything past the ceiling.
func parseTTL(s string, l Limits) (time.Duration, error) {
	if s == "" {
		return min(l.DefaultTTL, l.MaxTTL), nil
	}
	d, err := api.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d > l.MaxTTL {
		return 0, fmt.Errorf("ttl %s exceeds the maximum of %s", s, api.HumanDuration(l.MaxTTL))
	}
	return d, nil
}

func urlEscape(s string) string { return url.PathEscape(s) }

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }
