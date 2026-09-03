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

// parseTTL accepts Go durations plus the day and week suffixes people actually
// want, and clamps to the configured ceiling.
func parseTTL(s string, max time.Duration) (time.Duration, error) {
	if s == "" {
		return min(time.Hour, max), nil
	}
	var d time.Duration
	if last := s[len(s)-1]; last == 'd' || last == 'w' {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid ttl %q", s)
		}
		unit := 24 * time.Hour
		if last == 'w' {
			unit = 7 * 24 * time.Hour
		}
		d = time.Duration(n) * unit
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil || d <= 0 {
			return 0, fmt.Errorf("invalid ttl %q", s)
		}
	}
	if d > max {
		return 0, fmt.Errorf("ttl %s exceeds the maximum of %s", s, max)
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

// payload shapes retrieved items for the wire: text inline, files as one-shot
// URLs. Base64-ing a key file into an LLM agent's context is the wrong outcome,
// so file bytes stay out of band.
func payload(baseURL string, items []Item) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
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
