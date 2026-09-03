package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
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
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Error("no CSP header")
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
		Kind: KindRequest, Title: "t",
		Secrets:    []Secret{{Name: "one", Type: TypeText}},
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
