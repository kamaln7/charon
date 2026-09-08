package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/kamaln7/charon/internal/api"
)

// client is the thin HTTP layer. Every method returns the decoded body and a
// typed error carrying charon's own message, so callers never see raw JSON.
type client struct {
	base string
	http *http.Client
}

func newClient(base string) *client {
	if base == "" {
		base = "http://localhost:1337"
	}
	return &client{
		base: strings.TrimSuffix(base, "/"),
		// Long polls hold for up to a minute; leave headroom over that.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

// apiError is what charon answered with a non-2xx status.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.msg, e.status) }

func (c *client) do(method, path string, body io.Reader, ctype string, out any) error {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach charon at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		var e api.ErrorResponse
		if json.Unmarshal(raw, &e) != nil || e.Error == "" {
			e.Error = strings.TrimSpace(string(raw))
		}
		return &apiError{status: resp.StatusCode, msg: e.Error}
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *client) postJSON(path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do("POST", path, bytes.NewReader(b), "application/json", out)
}

func (c *client) create(kind api.Kind, req api.CreateRequest) (api.CreateResponse, error) {
	path := "/api/requests"
	if kind == api.KindSend {
		path = "/api/secrets"
	}
	var out api.CreateResponse
	err := c.postJSON(path, req, &out)
	return out, err
}

// view polls one entry, holding the request for up to wait when non-zero.
func (c *client) view(id string, wait time.Duration) (api.EntryResponse, error) {
	path := "/api/e/" + url.PathEscape(id)
	if wait > 0 {
		path += "?wait=" + url.QueryEscape(wait.String())
	}
	var out api.EntryResponse
	err := c.do("GET", path, nil, "", &out)
	return out, err
}

func (c *client) retrieve(id string) (api.RevealResponse, error) {
	var out api.RevealResponse
	err := c.do("POST", "/api/e/"+url.PathEscape(id)+"/retrieve", nil, "", &out)
	return out, err
}

func (c *client) setText(id string, idx int, text string) error {
	b, _ := json.Marshal(api.SetTextRequest{Text: text})
	return c.do("PUT", fmt.Sprintf("/api/e/%s/text/%d", url.PathEscape(id), idx),
		bytes.NewReader(b), "application/json", nil)
}

func (c *client) upload(id string, idx int, name string, r io.Reader) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile("file", filepath.Base(name))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, r); err != nil {
		return err
	}
	mw.Close()
	return c.do("POST", fmt.Sprintf("/api/e/%s/files/%d", url.PathEscape(id), idx),
		&buf, mw.FormDataContentType(), nil)
}

func (c *client) submit(id string) error {
	return c.do("POST", "/api/e/"+url.PathEscape(id)+"/submit", nil, "", nil)
}

func (c *client) download(fileURL string) ([]byte, error) {
	resp, err := c.http.Get(fileURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// tokenOf extracts the entry id from one of the URLs charon hands back.
func tokenOf(u string) string {
	return u[strings.LastIndex(strings.TrimSuffix(u, "/"), "/")+1:]
}

// decodeStrict mirrors the server: unknown fields are errors, so a typo in a
// spec fails here rather than becoming a different request.
func decodeStrict(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("stdin is not a valid spec: %w", err)
	}
	if dec.More() {
		return fmt.Errorf("stdin is not a valid spec: trailing content")
	}
	return nil
}
