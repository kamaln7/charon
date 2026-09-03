package main

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var ttlOptions = []string{"15m", "1h", "6h", "1d", "3d", "1w"}

// TTLOptions is the preset list the UI offers, minus anything the operator's
// ceiling forbids. Showing a choice the server would reject is a worse
// experience than showing fewer choices.
func (l Limits) TTLOptions() []string {
	var out []string
	for _, o := range ttlOptions {
		if d, err := parseDuration(o); err == nil && d <= l.MaxTTL {
			out = append(out, o)
		}
	}
	return out
}

// DefaultTTLOption returns the preset label matching the configured default,
// so the UI can pre-select a button rather than guessing: Go renders 24h as
// "24h0m0s", which matches no option the form offers.
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

// parseTTL accepts Go durations plus the day and week suffixes people actually
// want, defaults when omitted, and rejects anything past the ceiling.
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

// humanDuration renders a duration the way it would have been written. Go's
// own String() turns a 7-day cap into "168h0m0s", which is a poor thing to put
// in an error a person has to read.
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

// spool streams one multipart file part to scratch, enforcing the per-file cap
// and charging the bytes against the global ceiling as they land. Nothing is
// buffered in memory, so a client cannot make the process allocate past the
// limits by lying about Content-Length.
func (a *api) spool(part *multipart.Part) (File, error) {
	name := filepath.Base(part.FileName())
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "file"
	}
	fh, err := a.store.scratchFile()
	if err != nil {
		return File{}, err
	}
	limit := a.cfg.Limits.MaxFileBytes
	n, err := io.Copy(fh, io.LimitReader(part, limit+1))
	fh.Close()
	if err != nil {
		os.Remove(fh.Name())
		return File{}, err
	}
	if n > limit {
		os.Remove(fh.Name())
		return File{}, ErrTooLarge
	}
	if err := a.store.Reserve(n); err != nil {
		os.Remove(fh.Name())
		return File{}, err
	}
	return File{Name: name, Size: n, path: fh.Name()}, nil
}

func openFile(f File) (*os.File, error) { return os.Open(f.path) }

func urlEscape(s string) string { return url.PathEscape(s) }

// payload shapes retrieved secrets for the wire: text inline, files as one-shot
// URLs. Base64-ing a key file into an LLM agent's context is the wrong outcome,
// so file bytes stay out of band.
func payload(baseURL string, secrets []Secret) []map[string]any {
	out := make([]map[string]any, 0, len(secrets))
	for _, it := range secrets {
		m := map[string]any{"name": it.Name, "type": it.Type}
		if it.Text != "" {
			m["text"] = it.Text
		}
		if len(it.Files) > 0 {
			fs := make([]map[string]any, 0, len(it.Files))
			for _, f := range it.Files {
				fs = append(fs, map[string]any{
					"filename": f.Name,
					"size":     f.Size,
					"url":      baseURL + "/api/f/" + f.token,
				})
			}
			m["files"] = fs
		}
		out = append(out, m)
	}
	return out
}

// md leaves raw HTML escaped, which is goldmark's default. Descriptions are
// attacker-supplied the moment anyone can reach the create endpoint, so there
// is no WithUnsafe here and no sanitiser to configure wrongly.
var md = goldmark.New(goldmark.WithExtensions(extension.GFM))

func renderMarkdown(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := md.Convert([]byte(s), &buf); err != nil {
		return ""
	}
	return buf.String()
}
