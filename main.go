package main

import (
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// The scratch directory holds file bytes only. It is created fresh and
	// removed on exit: this service has no persistence, and a leftover
	// directory of half-delivered secrets would be exactly that.
	scratch := cfg.Scratch
	if scratch == "" {
		if scratch, err = os.MkdirTemp("", "charon-"); err != nil {
			log.Fatalf("scratch: %v", err)
		}
		defer os.RemoveAll(scratch)
	} else if err := os.MkdirAll(scratch, 0o700); err != nil {
		log.Fatalf("scratch: %v", err)
	}

	// Fail here rather than on the first upload. A tmpfs mounted over the
	// directory arrives owned by root, so an unprivileged container can end up
	// with a scratch path it cannot write to — which otherwise shows up much
	// later as one broken file part in an otherwise working submission.
	probe, err := os.CreateTemp(scratch, "probe-")
	if err != nil {
		log.Fatalf("scratch %s is not writable: %v", scratch, err)
	}
	probe.Close()
	os.Remove(probe.Name())

	store := NewStore(scratch, cfg.Limits)
	stop := make(chan struct{})
	go store.Run(stop)

	a := &api{store: store, cfg: cfg}
	mux := http.NewServeMux()
	a.routes(mux)

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}
	index, err := fs.ReadFile(static, "index.html")
	if err != nil {
		log.Fatalf("embed: %v", err)
	}
	files := http.FileServer(http.FS(static))

	// /e/<id> is a client-side route, so it has to return the app shell rather
	// than a 404. Everything else falls through to the embedded files.
	mux.HandleFunc("GET /e/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
	mux.Handle("GET /", files)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		close(stop)
		srv.Close()
	}()

	log.Printf("charon listening on %s (base %s, scratch %s)", cfg.Addr, cfg.BaseURL, scratch)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// securityHeaders keeps secrets out of caches and referrers, and pins the CSP
// tight enough that a stored-XSS in a description could not exfiltrate a
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
