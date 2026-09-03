package main

import (
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Scratch owns the directory holding uploaded file bytes.
//
// Filenames encode when the file stops being useful: "<unix expiry>-<random>".
// The in-memory store deletes files as entries die, but it only knows about
// entries this process created — a crash, a SIGKILL, or a container restart
// with a persistent scratch mount leaves orphans that nothing would ever
// collect. Encoding the deadline in the name means a sweep needs no state at
// all: read the directory, parse the prefix, delete what is past due. That is
// what makes a startup sweep possible.
type Scratch struct {
	dir string
}

func NewScratch(dir string) *Scratch { return &Scratch{dir: dir} }

func (s *Scratch) Dir() string { return s.dir }

// Create opens a new scratch file that becomes collectable after expires.
func (s *Scratch) Create(expires time.Time) (*os.File, error) {
	return os.Create(filepath.Join(s.dir, fmt.Sprintf("%d-%s", expires.Unix(), NewID())))
}

func (s *Scratch) Remove(path string) {
	if path != "" {
		os.Remove(path)
	}
}

// Sweep deletes every file whose encoded expiry has passed. Safe to run on a
// timer and on startup; returns how many it removed.
func (s *Scratch) Sweep(now time.Time) int {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		log.Printf("scratch sweep: %v", err)
		return 0
	}
	var removed, unknown int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		expiry, ok := expiryFromName(e.Name())
		if !ok {
			// Not ours. Leave it rather than deleting files we cannot explain.
			unknown++
			continue
		}
		if now.After(expiry) {
			s.Remove(filepath.Join(s.dir, e.Name()))
			removed++
		}
	}
	if unknown > 0 {
		log.Printf("scratch sweep: ignored %d unrecognised file(s) in %s", unknown, s.dir)
	}
	return removed
}

func expiryFromName(name string) (time.Time, bool) {
	prefix, _, found := strings.Cut(name, "-")
	if !found {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// Writable fails fast if the directory cannot be written to. A tmpfs mounted
// over it arrives owned by root, so an unprivileged container can end up with
// a scratch path it cannot use — which otherwise surfaces much later as one
// broken file in an otherwise working submission.
func (s *Scratch) Writable() error {
	f, err := s.Create(time.Now())
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// Spool streams one multipart part to disk, enforcing the per-file cap and
// charging the bytes against the global ceiling as they land. Nothing is
// buffered in memory, so a client cannot make the process allocate past the
// limits by lying about Content-Length.
func (s *Scratch) Spool(part *multipart.Part, expires time.Time, limit int64, reserve func(int64) error) (File, error) {
	f, err := s.Create(expires)
	if err != nil {
		return File{}, err
	}
	n, err := io.Copy(f, io.LimitReader(part, limit+1))
	f.Close()
	if err != nil {
		s.Remove(f.Name())
		return File{}, err
	}
	if n > limit {
		s.Remove(f.Name())
		return File{}, ErrTooLarge
	}
	if err := reserve(n); err != nil {
		s.Remove(f.Name())
		return File{}, err
	}
	return File{Name: safeFilename(part.FileName()), Size: n, path: f.Name()}, nil
}

// safeFilename keeps the client's name as metadata only; the path on disk is
// always one we generated.
func safeFilename(name string) string {
	name = filepath.Base(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "file"
	}
	return name
}
