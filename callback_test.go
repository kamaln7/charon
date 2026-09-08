package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	rulekit "github.com/qpoint-io/rulekit/v2"
)

func ruleFor(t *testing.T, expr string) Callback {
	t.Helper()
	r, err := rulekit.Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	return Callback{Rule: r}
}

// With no rule configured the feature is off. "Off" must mean deny, not allow:
// getting this backwards turns the service into an open request proxy.
func TestCallbackDisabledByDefault(t *testing.T) {
	var c Callback
	if c.Enabled() {
		t.Fatal("a zero Callback reports itself enabled")
	}
	if err := c.Check("http://hooks.internal:8080/hook"); err == nil {
		t.Fatal("callback allowed with no rule configured")
	}
}

func TestCallbackRuleMatching(t *testing.T) {
	c := ruleFor(t, `hostname == "hooks.internal" and port == 8080`)

	for _, ok := range []string{
		"http://hooks.internal:8080/callback",
		"http://hooks.internal:8080/",
	} {
		if err := c.Check(ok); err != nil {
			t.Errorf("Check(%q) = %v, want allowed", ok, err)
		}
	}
	for _, bad := range []string{
		"http://hooks.internal:8081/callback",      // wrong port
		"http://hooks.internal/callback",           // port defaults to 80, not 8080
		"http://evil.example/callback",             // wrong host
		"http://hooks.internal:8080@evil.example/", // userinfo trick: host is evil.example
	} {
		if err := c.Check(bad); err == nil {
			t.Errorf("Check(%q) = allowed, want rejected", bad)
		}
	}
}

// A rule naming a field the URL cannot supply is Unknown, not false. Treating
// Unknown as a pass would let a rule silently stop constraining anything.
func TestCallbackFailsClosedOnUnknownField(t *testing.T) {
	c := ruleFor(t, `nonexistent_field == "x"`)
	err := c.Check("http://hooks.internal:8080/hook")
	if err == nil {
		t.Fatal("a rule on a missing field allowed the callback")
	}
	if !strings.Contains(err.Error(), "does not provide") {
		t.Fatalf("want a missing-fields error, got %v", err)
	}
}

// `true` is the documented allow-all escape hatch.
func TestCallbackAllowAllRule(t *testing.T) {
	c := ruleFor(t, `true`)
	if err := c.Check("https://anything.example/hook"); err != nil {
		t.Fatalf("allow-all rule rejected a URL: %v", err)
	}
	// Even allow-all refuses a non-HTTP scheme: file:// and gopher:// are not
	// things a webhook should ever reach.
	for _, bad := range []string{"file:///etc/passwd", "gopher://x/", "not a url at all"} {
		if err := c.Check(bad); err == nil {
			t.Errorf("Check(%q) = allowed under allow-all, want rejected", bad)
		}
	}
}

func TestCallbackCIDRRule(t *testing.T) {
	c := ruleFor(t, `ip in 192.168.0.0/16`)
	if err := c.Check("http://192.168.0.10:8080/hook"); err != nil {
		t.Errorf("in-range literal IP rejected: %v", err)
	}
	if err := c.Check("http://10.0.0.1:8080/hook"); err == nil {
		t.Error("out-of-range IP allowed")
	}
	// A hostname publishes no ip field, so a CIDR rule cannot match it. This is
	// deliberate: resolving here would let a name pass the rule and then point
	// somewhere else at delivery time.
	if err := c.Check("http://hooks.internal:8080/hook"); err == nil {
		t.Error("hostname matched a CIDR rule; DNS must not be resolved")
	}
}

func TestCallbackKVFields(t *testing.T) {
	u, err := url.Parse("https://user@hook.example:8443/hook/path?a=1#frag")
	if err != nil {
		t.Fatal(err)
	}
	kv := callbackKV(u)
	for field, want := range map[string]any{
		"scheme":   "https",
		"host":     "hook.example:8443",
		"hostname": "hook.example",
		"port":     8443,
		"path":     "/hook/path",
		"query":    "a=1",
		"fragment": "frag",
		"user":     "user",
	} {
		if kv[field] != want {
			t.Errorf("kv[%q] = %v, want %v", field, kv[field], want)
		}
	}
	if _, ok := kv["ip"]; ok {
		t.Error("a hostname produced an ip field")
	}
}

func TestCallbackRefusesRedirects(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer evil.Close()
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL, http.StatusFound)
	}))
	defer hop.Close()
	if err := (Callback{}).post(hop.URL, []byte(`{}`)); err == nil {
		t.Fatal("followed a redirect")
	}
}

func TestCallbackSend(t *testing.T) {
	s := NewStore(testLimits())
	e := newTestEntry(s, time.Hour)
	s.SetText(e, 0, "payload")
	s.Submit(e)

	var body []byte
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	e.CallbackURL = srv.URL
	Callback{Secret: "k"}.Send(s, "http://x", e)
	if e.ConsumedAt.IsZero() {
		t.Fatal("success did not start linger")
	}
	if !strings.Contains(string(body), "payload") {
		t.Fatalf("body %s", body)
	}
	if strings.Contains(string(body), `"destructs_at":"0001-`) {
		t.Fatalf("destructs_at unset: %s", body)
	}
	mac := hmac.New(sha256.New, []byte("k"))
	mac.Write([]byte(hdr.Get("X-Charon-Timestamp") + "."))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if hdr.Get("X-Charon-Signature") != want {
		t.Fatalf("signature %q, want %q", hdr.Get("X-Charon-Signature"), want)
	}
}

// Ports must be comparable numerically even when the URL omits them.
func TestCallbackDefaultPorts(t *testing.T) {
	for raw, want := range map[string]int{
		"http://h/":       80,
		"https://h/":      443,
		"http://h:8080/":  8080,
		"https://h:8443/": 8443,
	} {
		u, _ := url.Parse(raw)
		if got := urlPort(u); got != want {
			t.Errorf("urlPort(%q) = %d, want %d", raw, got, want)
		}
	}
}
