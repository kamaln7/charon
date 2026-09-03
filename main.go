package main

import (
	"context"
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
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	scratch, cleanup, err := openScratch(cfg.Scratch)
	if err != nil {
		return err
	}
	defer cleanup()

	// Sweep before serving. With a persistent scratch mount, a crash or a
	// SIGKILL leaves files behind that no in-memory entry refers to any more;
	// their encoded expiry is the only thing that can still identify them.
	if n := scratch.Sweep(time.Now()); n > 0 {
		log.Printf("startup: removed %d expired scratch file(s)", n)
	}

	store := NewStore(cfg.Limits)

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	srv, err := newServer(cfg, store, scratch, web)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go reap(ctx, store, scratch)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		httpSrv.Close()
	}()

	log.Printf("charon listening on %s (base %s, scratch %s)", cfg.Addr, cfg.BaseURL, scratch.Dir())
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// openScratch prepares the directory holding file bytes. An unconfigured path
// gets a private temp directory removed on exit: this service has no
// persistence, and a leftover directory of half-delivered secrets would be
// exactly that.
func openScratch(dir string) (*Scratch, func(), error) {
	cleanup := func() {}
	if dir == "" {
		tmp, err := os.MkdirTemp("", "charon-")
		if err != nil {
			return nil, cleanup, err
		}
		dir, cleanup = tmp, func() { os.RemoveAll(tmp) }
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, cleanup, err
	}
	s := NewScratch(dir)
	if err := s.Writable(); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return s, cleanup, nil
}

// reap expires entries and collects orphaned files on the same tick.
func reap(ctx context.Context, store *Store, scratch *Scratch) {
	entries := time.NewTicker(time.Second)
	defer entries.Stop()
	// Files are deleted as their entries die, so this pass only catches
	// orphans. Once a minute is plenty.
	files := time.NewTicker(time.Minute)
	defer files.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-entries.C:
			for _, path := range store.Sweep(now) {
				scratch.Remove(path)
			}
		case now := <-files.C:
			scratch.Sweep(now)
		}
	}
}
