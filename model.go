package main

import (
	"bytes"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Every request and response body the API speaks. Nothing here is built from
// map[string]any: the wire shape is the contract, so it lives in types.

// ---------- requests ----------

// SecretSpec describes one requested or supplied secret. Every field is
// optional — an unnamed secret is numbered, and a description only helps when
// you are asking someone else for something.
type SecretSpec struct {
	Name        string     `json:"name,omitempty"`
	Description string     `json:"description,omitempty"`
	Type        SecretType `json:"type,omitempty"`
}

type CreateRequest struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Secrets     []SecretSpec `json:"secrets"`
	TTL         string       `json:"ttl,omitempty"`
	CallbackURL string       `json:"callback_url,omitempty"`
}

type SetTextRequest struct {
	Text string `json:"text"`
}

// ---------- responses ----------

type CreateResponse struct {
	Title       string `json:"title"`
	SubmitURL   string `json:"submit_url"`
	RetrieveURL string `json:"retrieve_url"`
	// PollURL is the view endpoint under another name, so a caller reading the
	// response sees the flow: poll it until fulfilled, then POST RetrieveURL.
	PollURL string `json:"poll_url"`
	// ManageURL carries the owner token. Creation redirects here so the two
	// links survive a refresh instead of living only in page state.
	ManageURL string `json:"manage_url"`
	ExpiresAt string `json:"expires_at"`
}

// EntryResponse is the non-secret view of an entry. Draft values appear only
// for the submit token; the retrieve side gets values by consuming the entry.
type EntryResponse struct {
	Kind      Kind             `json:"kind"`
	Role      string           `json:"role"`
	Title     string           `json:"title"`
	HTML      string           `json:"description_html,omitempty"`
	Secrets   []SecretResponse `json:"secrets"`
	Fulfilled bool             `json:"fulfilled"`
	Retrieved bool             `json:"retrieved"`
	ExpiresAt string           `json:"expires_at"`

	// Only populated for the manage role: the links the owner hands out.
	SubmitURL   string `json:"submit_url,omitempty"`
	RetrieveURL string `json:"retrieve_url,omitempty"`
}

type SecretResponse struct {
	Name  string     `json:"name"`
	HTML  string     `json:"description_html,omitempty"`
	Type  SecretType `json:"type"`
	Text  string     `json:"text,omitempty"`
	Files []string   `json:"files,omitempty"`
}

// RevealResponse is the payload. Text is inline; files are one-shot URLs,
// because base64-ing a key file into an agent's context is the wrong outcome.
type RevealResponse struct {
	Title       string           `json:"title"`
	Secrets     []RevealedSecret `json:"secrets"`
	DestructsAt string           `json:"destructs_at"`
}

type RevealedSecret struct {
	Name  string         `json:"name"`
	Type  SecretType     `json:"type"`
	Text  string         `json:"text,omitempty"`
	Files []FileResponse `json:"files,omitempty"`
}

type FileResponse struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

type ConfigResponse struct {
	MaxFileBytes  int64    `json:"max_file_bytes"`
	MaxTextBytes  int64    `json:"max_text_bytes"`
	MaxSecrets    int      `json:"max_secrets"`
	TTLOptions    []string `json:"ttl_options"`
	DefaultTTL    string   `json:"default_ttl"`
	LingerSeconds int      `json:"linger_seconds"`
	Callbacks     bool     `json:"callbacks"`
}

type UploadResponse struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type OKResponse struct {
	OK bool `json:"ok"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// ---------- conversions ----------

func (c Config) createResponse(e *Entry) CreateResponse {
	return CreateResponse{
		Title:       e.Title,
		SubmitURL:   c.BaseURL + "/e/" + e.SubmitID,
		RetrieveURL: c.BaseURL + "/e/" + e.RetrieveID,
		PollURL:     c.BaseURL + "/api/e/" + e.RetrieveID,
		ManageURL:   c.BaseURL + "/manage?token=" + e.ManageID,
		ExpiresAt:   rfc3339(e.ExpiresAt),
	}
}

func (c Config) entryResponse(e *Entry, rl role) EntryResponse {
	out := EntryResponse{
		Kind:      e.Kind,
		Role:      rl.String(),
		Title:     e.Title,
		HTML:      renderMarkdown(e.Description),
		Secrets:   make([]SecretResponse, 0, len(e.Secrets)),
		Fulfilled: e.Fulfilled,
		Retrieved: !e.ConsumedAt.IsZero(),
		ExpiresAt: rfc3339(e.ExpiresAt),
	}
	if rl == roleManage {
		out.SubmitURL = c.BaseURL + "/e/" + e.SubmitID
		out.RetrieveURL = c.BaseURL + "/e/" + e.RetrieveID
	}
	draft := rl == roleSubmit && !e.Fulfilled
	for _, sec := range e.Secrets {
		s := SecretResponse{Name: sec.Name, HTML: renderMarkdown(sec.Description), Type: sec.Type}
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

func revealResponse(baseURL, title string, destructsAt time.Time, secrets []Secret) RevealResponse {
	out := RevealResponse{
		Title:       title,
		Secrets:     make([]RevealedSecret, 0, len(secrets)),
		DestructsAt: rfc3339(destructsAt),
	}
	for _, sec := range secrets {
		r := RevealedSecret{Name: sec.Name, Type: sec.Type, Text: sec.Text}
		for _, f := range sec.Files {
			r.Files = append(r.Files, FileResponse{
				Filename: f.Name,
				Size:     f.Size,
				URL:      baseURL + "/api/f/" + f.token,
			})
		}
		out.Secrets = append(out.Secrets, r)
	}
	return out
}

func (c Config) configResponse() ConfigResponse {
	return ConfigResponse{
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
