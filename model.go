package main

import (
	"bytes"
	"strings"
	"time"

	"github.com/kamaln7/charon/internal/api"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// ---------- conversions ----------

func (c Config) createResponse(e *Entry) api.CreateResponse {
	return api.CreateResponse{
		Title:       e.Title,
		SubmitURL:   c.BaseURL + "/e/" + e.SubmitID,
		RetrieveURL: c.BaseURL + "/e/" + e.RetrieveID,
		PollURL:     c.BaseURL + "/api/e/" + e.RetrieveID,
		ManageURL:   c.BaseURL + "/manage?token=" + e.ManageID,
		DestroyURL:  c.BaseURL + "/api/e/" + e.ManageID,
		SubmitID:    e.SubmitID,
		RetrieveID:  e.RetrieveID,
		ManageID:    e.ManageID,
		ExpiresAt:   rfc3339(e.ExpiresAt),
	}
}

func (c Config) entryResponse(e *Entry, rl role) api.EntryResponse {
	out := api.EntryResponse{
		Kind:          e.Kind,
		Role:          rl.String(),
		Title:         e.Title,
		HTML:          renderMarkdown(e.Description),
		Secrets:       make([]api.SecretResponse, 0, len(e.Secrets)),
		Fulfilled:     e.Fulfilled,
		Retrieved:     !e.ConsumedAt.IsZero(),
		ExpiresAt:     rfc3339(e.ExpiresAt),
		LingerSeconds: int(e.Linger.Seconds()),
	}
	if rl == roleManage {
		out.SubmitURL = c.BaseURL + "/e/" + e.SubmitID
		out.RetrieveURL = c.BaseURL + "/e/" + e.RetrieveID
	}
	draft := rl == roleSubmit && !e.Fulfilled
	for _, sec := range e.Secrets {
		s := api.SecretResponse{Name: sec.Name, HTML: renderMarkdown(sec.Description), Type: sec.Type}
		if draft {
			s.Text = sec.Text
			for _, f := range sec.Files {
				s.Files = append(s.Files, f.Name)
			}
		}
		out.Secrets = append(out.Secrets, s)
	}
	return out
}

func revealResponse(baseURL, title string, destructsAt time.Time, secrets []Secret) api.RevealResponse {
	out := api.RevealResponse{
		Title:       title,
		Secrets:     make([]api.RevealedSecret, 0, len(secrets)),
		DestructsAt: rfc3339(destructsAt),
	}
	for _, sec := range secrets {
		r := api.RevealedSecret{Name: sec.Name, Type: sec.Type}
		if sec.Text != "" {
			text := sec.Text
			r.Text = &text
		}
		for _, f := range sec.Files {
			r.Files = append(r.Files, api.FileResponse{
				Filename: f.Name,
				Size:     f.Size,
				URL:      baseURL + "/api/f/" + f.token,
			})
		}
		out.Secrets = append(out.Secrets, r)
	}
	return out
}

func (c Config) configResponse() api.ConfigResponse {
	return api.ConfigResponse{
		MaxFileBytes:  c.Limits.MaxFileBytes,
		MaxTextBytes:  c.Limits.MaxTextBytes,
		MaxSecrets:    c.Limits.MaxSecrets,
		TTLOptions:    c.Limits.TTLOptions(),
		DefaultTTL:    c.Limits.DefaultTTLOption(),
		LingerSeconds: int(c.Limits.Linger.Seconds()),
		Callbacks:     c.Callback.Enabled(),
	}
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// renderMarkdown leaves raw HTML escaped, which is goldmark's default.
// Descriptions are attacker-supplied the moment anyone can reach the create
// endpoint, so there is no WithUnsafe and no sanitiser to misconfigure.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

func renderMarkdown(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := markdown.Convert([]byte(s), &buf); err != nil {
		return ""
	}
	return buf.String()
}
