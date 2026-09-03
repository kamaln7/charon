package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	rulekit "github.com/qpoint-io/rulekit/v2"
)

// Callback pushes a fulfilled entry to a URL the creator supplied, so an agent
// does not have to sit in a polling loop.
//
// A caller-supplied URL is a "make my server issue a request" primitive, so the
// feature stays off until an operator writes a rule describing what is allowed.
type Callback struct {
	Rule   rulekit.Rule
	Secret string
	Client *http.Client
}

func (c Callback) Enabled() bool { return c.Rule != nil }

// Check evaluates the operator's rule against the URL's components. It fails
// closed: only an unambiguous pass allows delivery.
func (c Callback) Check(raw string) error {
	if c.Rule == nil {
		return fmt.Errorf("callbacks are disabled; set CHARON_CALLBACK_RULE to enable them")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme %q is not http or https", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL has no host")
	}

	res := c.Rule.Eval(context.Background(), rulekit.FromKV(callbackKV(u)), rulekit.Opts{})
	switch {
	case res.Error != nil:
		return fmt.Errorf("callback rule error: %w", res.Error)
	case res.Unknown():
		return fmt.Errorf("callback rule needs fields this URL does not provide: %v", res.MissingFields)
	case res.Pass():
		return nil
	default:
		return fmt.Errorf("URL does not match the configured callback rule")
	}
}

// callbackKV publishes the URL's accessors as top-level rule fields, so a rule
// reads `hostname == "hermes" and port == 9119`.
func callbackKV(u *url.URL) rulekit.KV {
	kv := rulekit.KV{
		"url":      u.String(),
		"scheme":   u.Scheme,
		"host":     u.Host,
		"hostname": u.Hostname(),
		"port":     urlPort(u),
		"path":     u.Path,
		"query":    u.RawQuery,
		"fragment": u.Fragment,
		"user":     u.User.Username(),
	}
	// Only a literal address becomes an IP field. Resolving a name here would
	// be a rebinding hole: the rule would pass against one address and the
	// delivery would then connect to whatever DNS returned a moment later.
	if ip := net.ParseIP(u.Hostname()); ip != nil {
		kv["ip"] = ip
	}
	return kv
}

// urlPort reports the port a request would actually use, so a rule can compare
// it numerically without every URL having to spell it out.
func urlPort(u *url.URL) int {
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return n
		}
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}

// Send posts the payload, retrying a few times. Success consumes the entry and
// starts its self-destruct timer; if every attempt fails the entry is released
// again, so a webhook that happened to be down does not destroy the secret.
func (c Callback) Send(store *Store, baseURL string, e *Entry) {
	secrets, err := store.Retrieve(e)
	if err != nil {
		return
	}
	body, err := json.Marshal(map[string]any{
		"title":        e.Title,
		"secrets":      payload(baseURL, secrets),
		"destructs_at": e.ConsumedAt.Add(store.limits.Linger).UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}

	for attempt := range 3 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
		if err := c.post(e.CallbackURL, body); err != nil {
			log.Printf("callback attempt %d/3 failed: %v", attempt+1, err)
			continue
		}
		return
	}
	log.Printf("callback gave up; the retrieve link remains valid")
	store.Unconsume(e)
}

func (c Callback) post(target string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Secret != "" {
		// Timestamp and body are signed together so a receiver can reject both
		// forgeries and replays.
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmacSHA256([]byte(c.Secret), append([]byte(ts+"."), body...))
		req.Header.Set("X-Charon-Timestamp", ts)
		req.Header.Set("X-Charon-Signature", "sha256="+hex.EncodeToString(mac))
	}

	res, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}
