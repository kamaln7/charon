package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"runtime/debug"
)

type server struct {
	cfg     Config
	store   *Store
	scratch *Scratch
	index   []byte
	static  http.Handler
}

func newServer(cfg Config, store *Store, scratch *Scratch, web fs.FS) (*server, error) {
	index, err := fs.ReadFile(web, "index.html")
	if err != nil {
		return nil, err
	}
	return &server{
		cfg:     cfg,
		store:   store,
		scratch: scratch,
		index:   index,
		static:  http.FileServer(http.FS(web)),
	}, nil
}

// Handler wires the routes and the middleware chain.
//
// The API is unauthenticated by design: possession of a link is the only
// credential, and charon is meant to sit on a trusted network or behind a
// proxy that authenticates.
func (s *server) Handler() http.Handler {
	api := http.NewServeMux()
	api.Handle("GET /api/config", s.handle(s.getConfig))
	api.Handle("POST /api/requests", s.handle(s.create(KindRequest)))
	api.Handle("POST /api/secrets", s.handle(s.create(KindSend)))
	api.Handle("GET /api/e/{id}", s.handle(s.viewEntry))
	api.Handle("PUT /api/e/{id}/text/{idx}", s.handle(s.setText))
	api.Handle("POST /api/e/{id}/files/{idx}", s.handle(s.addFile))
	api.Handle("DELETE /api/e/{id}/files/{idx}/{n}", s.handle(s.dropFile))
	api.Handle("POST /api/e/{id}/submit", s.handle(s.submitEntry))
	api.Handle("POST /api/e/{id}/retrieve", s.handle(s.retrieveEntry))
	api.Handle("GET /api/f/{token}", s.handle(s.downloadFile))

	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	// /e/<id> is a client-side route, so it serves the app shell rather than a
	// 404. Everything else falls through to the embedded assets.
	mux.HandleFunc("GET /e/{id}", s.serveIndex)
	// No method on this one: a method-qualified "GET /" would conflict with
	// the method-less "/api/" above, which ServeMux rejects at registration.
	mux.Handle("/", s.static)

	return securityHeaders(recoverPanics(logProblems(mux)))
}

func (s *server) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(s.index)
}

// ---------- handler plumbing ----------

// handlerFunc returns an error instead of writing one, so handlers read as a
// straight line of `if err != nil { return err }` rather than repeating the
// write-then-return dance at every branch.
type handlerFunc func(http.ResponseWriter, *http.Request) error

func (s *server) handle(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			writeError(w, err)
		}
	})
}

// statusError carries an HTTP status alongside the message. Store-level
// sentinels map to statuses in writeError, so handlers can return either.
type statusError struct {
	code int
	msg  string
}

func (e statusError) Error() string { return e.msg }

func errorf(code int, format string, args ...any) error {
	return statusError{code: code, msg: sprintf(format, args...)}
}

var errNotFound = statusError{code: http.StatusNotFound, msg: "not found"}

func writeError(w http.ResponseWriter, err error) {
	var se statusError
	if errors.As(err, &se) {
		writeJSON(w, se.code, ErrorResponse{Error: se.msg})
		return
	}
	writeJSON(w, statusFor(err), ErrorResponse{Error: err.Error()})
}

// statusFor maps the store's sentinel errors onto HTTP.
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrFull):
		return http.StatusInsufficientStorage
	case errors.Is(err, ErrPending), errors.Is(err, ErrFulfilled):
		return http.StatusConflict
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// ---------- middleware ----------

// statusWriter remembers what was written so the logger can see it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// logProblems logs failed requests only. Successes are the overwhelming
// majority and say nothing; logging them would bury the ones that matter and
// build a browsable index of live secret IDs in the process.
func logProblems(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status >= 400 {
			log.Printf("%d %s %s", sw.status, r.Method, r.URL.Path)
		}
	})
}

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is the documented way to drop a connection
			// on purpose; re-panic so the server handles it as intended.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			log.Printf("panic: %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "internal error"})
		}()
		next.ServeHTTP(w, r)
	})
}

// securityHeaders keeps secrets out of caches and referrers, and pins the CSP
// tight enough that a stored XSS in a description could not exfiltrate a
// revealed secret. telegram.org is allowed because the Mini App bridge script
// has to come from there.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' https://telegram.org; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"frame-ancestors https://web.telegram.org; " +
		"base-uri 'none'; form-action 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
