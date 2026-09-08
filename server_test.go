package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kamaln7/charon/internal/api"
)

func testServer(t *testing.T) *server {
	t.Helper()
	cfg := Config{BaseURL: "http://x", Limits: testLimits()}
	web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>ok</html>")}}
	s, err := newServer(cfg, NewStore(cfg.Limits), NewScratch(t.TempDir()), web)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Building the handler is itself the assertion: ServeMux panics on conflicting
// patterns at registration, so this fails the moment a route overlaps another.
func TestHandlerRoutesRegister(t *testing.T) {
	h := testServer(t).Handler()

	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/api/config", http.StatusOK},
		{"GET", "/api/e/nope", http.StatusNotFound},
		{"POST", "/api/e/nope/retrieve", http.StatusNotFound},
		{"GET", "/e/anything", http.StatusOK}, // client-side route: app shell
		{"GET", "/", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	testServer(t).Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
	for h, want := range map[string]string{
		"Cache-Control":          "no-store",
		"Referrer-Policy":        "no-referrer",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := rec.Header().Get(h); got != want {
			t.Errorf("%s = %q, want %q", h, got, want)
		}
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self'") || !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("CSP = %q", csp)
	}
}

// A panicking handler must produce a 500, not a dropped connection.
func TestRecoverPanics(t *testing.T) {
	h := recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// http.ErrAbortHandler is the documented way to drop a connection deliberately;
// swallowing it would turn an intentional abort into a bogus 500.
func TestRecoverRepanicsOnAbort(t *testing.T) {
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want ErrAbortHandler to propagate", rec)
		}
	}()
	h := recoverPanics(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

// The two tokens must not be interchangeable over HTTP either: a submit token
// posted at the retrieve route is the exact confusion that would hand a secret
// back to whoever was asked for it.
func TestSubmitTokenCannotRetrieve(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	e := &Entry{
		Kind: api.KindRequest, Title: "t",
		Secrets:    []Secret{{Name: "one", Type: api.TypeText}},
		SubmitID:   NewID(),
		RetrieveID: NewID(),
		ExpiresAt:  timeNowPlusHour(),
	}
	s.store.Put(e)
	s.store.Submit(e)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/e/"+e.SubmitID+"/retrieve", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("submit token retrieved: status %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/e/"+e.RetrieveID+"/retrieve", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("retrieve token rejected: status %d, want 200", rec.Code)
	}
}

// The manage token reads back the other two links, but must not be able to
// act with them. If it could submit or retrieve, the owner's bookmark would
// become a way to consume the secret by accident.
func TestManageTokenIsReadOnly(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	e := &Entry{
		Kind: api.KindRequest, Title: "t",
		Secrets:    []Secret{{Name: "one", Type: api.TypeText}},
		SubmitID:   NewID(),
		RetrieveID: NewID(),
		ManageID:   NewID(),
		ExpiresAt:  timeNowPlusHour(),
	}
	s.store.Put(e)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/e/"+e.ManageID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("manage view: status %d, want 200", rec.Code)
	}
	var got api.EntryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Role != "manage" {
		t.Errorf("role = %q, want manage", got.Role)
	}
	if got.SubmitURL == "" || got.RetrieveURL == "" {
		t.Error("manage view withheld the links it exists to show")
	}

	for _, path := range []string{"/submit", "/retrieve"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/e/"+e.ManageID+path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("manage token POST%s = %d, want 404", path, rec.Code)
		}
	}
	// It must not leak draft values either; that is the submitter's view.
	s.store.SetText(e, 0, "drafted")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/e/"+e.ManageID, nil))
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Secrets[0].Text != "" {
		t.Errorf("manage view exposed the draft value %q", got.Secrets[0].Text)
	}
}

// The creator declares what each secret is. The API must hold them to it: a
// file accepted onto a text secret is how a request for one token came back
// with an unrelated file attached.
func TestSecretTypeIsEnforced(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	e := &Entry{
		Kind: api.KindRequest, Title: "t",
		Secrets:    []Secret{{Name: "tok", Type: api.TypeText}, {Name: "key", Type: api.TypeFile}},
		SubmitID:   NewID(),
		RetrieveID: NewID(),
		ManageID:   NewID(),
		ExpiresAt:  timeNowPlusHour(),
	}
	s.store.Put(e)
	base := "/api/e/" + e.SubmitID

	// Text onto the text secret: allowed. Text onto the file secret: refused.
	if code := do(h, "PUT", base+"/text/0", `{"text":"v"}`); code != http.StatusNoContent {
		t.Errorf("text to a text secret = %d, want 204", code)
	}
	if code := do(h, "PUT", base+"/text/1", `{"text":"v"}`); code != http.StatusBadRequest {
		t.Errorf("text to a file secret = %d, want 400", code)
	}
	// A file part onto the text secret must be refused before it is spooled.
	if code := postFile(h, base+"/files/0"); code != http.StatusBadRequest {
		t.Errorf("file to a text secret = %d, want 400", code)
	}
	if code := postFile(h, base+"/files/1"); code != http.StatusOK {
		t.Errorf("file to a file secret = %d, want 200", code)
	}
}

func TestCreateRejectsBadSchema(t *testing.T) {
	h := testServer(t).Handler()
	for name, body := range map[string]string{
		"unknown top-level field": `{"secrets":[{}],"ttlx":"1h"}`,
		"unknown secret field":    `{"secrets":[{"kind":"text"}]}`,
		"misspelled secrets key":  `{"secret":[{}]}`,
		"bad secret type":         `{"secrets":[{"type":"flie"}]}`,
		"trailing content":        `{"secrets":[{}]}{"secrets":[{}]}`,
		"title too long":          `{"secrets":[{}],"title":"` + strings.Repeat("x", 201) + `"}`,
		"name too long":           `{"secrets":[{"name":"` + strings.Repeat("x", 201) + `"}]}`,
		"no secrets":              `{"secrets":[]}`,
		"not an object":           `["secrets"]`,
	} {
		if code := do(h, "POST", "/api/requests", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	// The shapes that must still work.
	for name, body := range map[string]string{
		"bare minimum":    `{"secrets":[{}]}`,
		"explicit text":   `{"secrets":[{"type":"text"}]}`,
		"explicit file":   `{"secrets":[{"type":"file"}]}`,
		"every field set": `{"title":"t","description":"d","ttl":"1h","secrets":[{"name":"n","description":"d","type":"file"}]}`,
	} {
		if code := do(h, "POST", "/api/requests", body); code != http.StatusCreated {
			t.Errorf("%s: status %d, want 201", name, code)
		}
	}
}

func do(h http.Handler, method, path, body string) int {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func postFile(h http.Handler, path string) int {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile("file", "k.pem")
	w.Write([]byte("data"))
	mw.Close()

	r := httptest.NewRequest("POST", path, &buf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

// ?wait= must return the moment the other side submits, not at the end of the
// window; otherwise it is polling with extra steps.
func TestViewWaitReturnsOnSubmit(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	e := &Entry{
		Kind: api.KindRequest, Title: "t",
		Secrets:    []Secret{{Name: "one", Type: api.TypeText}},
		SubmitID:   NewID(),
		RetrieveID: NewID(),
		ExpiresAt:  timeNowPlusHour(),
	}
	s.store.Put(e)

	done := make(chan api.EntryResponse, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/e/"+e.RetrieveID+"?wait=10s", nil))
		var got api.EntryResponse
		json.Unmarshal(rec.Body.Bytes(), &got)
		done <- got
	}()

	time.Sleep(50 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("wait returned before submission")
	default:
	}
	s.store.Submit(e)
	select {
	case got := <-done:
		if !got.Fulfilled {
			t.Error("woke up but not fulfilled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return after submission")
	}
}

// An unanswered wait ends at the requested window with the ordinary view, so
// a client can simply loop. A bad value is a 400, and an oversized one is
// clamped rather than refused.
func TestViewWaitTimesOutAndValidates(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	e := &Entry{
		Kind: api.KindRequest, Title: "t",
		Secrets:    []Secret{{Name: "one", Type: api.TypeText}},
		SubmitID:   NewID(),
		RetrieveID: NewID(),
		ExpiresAt:  timeNowPlusHour(),
	}
	s.store.Put(e)

	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/e/"+e.RetrieveID+"?wait=100ms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if el := time.Since(start); el < 100*time.Millisecond || el > 2*time.Second {
		t.Errorf("wait lasted %v, want about 100ms", el)
	}
	var got api.EntryResponse
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Fulfilled {
		t.Error("fulfilled without a submission")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/e/"+e.RetrieveID+"?wait=soon", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad wait = %d, want 400", rec.Code)
	}
	if d, _ := parseWait("1h"); d != maxWait {
		t.Errorf("wait=1h clamped to %v, want %v", d, maxWait)
	}
}

// Destroying an entry must wake its waiters, or a sweep during a long poll
// leaves a goroutine holding a connection until the window ends.
func TestViewWaitWakesOnDestroy(t *testing.T) {
	s := testServer(t)
	h := s.Handler()
	e := &Entry{
		Kind: api.KindRequest, Title: "t",
		Secrets:    []Secret{{Name: "one", Type: api.TypeText}},
		SubmitID:   NewID(),
		RetrieveID: NewID(),
		ExpiresAt:  timeNowPlusHour(),
	}
	s.store.Put(e)

	codes := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/e/"+e.RetrieveID+"?wait=10s", nil))
		codes <- rec.Code
	}()
	time.Sleep(50 * time.Millisecond)
	s.store.destroy(e)
	select {
	case code := <-codes:
		if code != http.StatusNotFound {
			t.Errorf("status after destroy = %d, want 404", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not return after destroy")
	}
}
