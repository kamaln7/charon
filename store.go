package main

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"os"
	"sync"
	"time"
)

// Kind is cosmetic: it tells the frontend which side of the link the visitor is
// on. Both modes are the same machine underneath — an entry with a submit token
// and a retrieve token. In request mode you hand out the submit link; in send
// mode you fill it in yourself and hand out the retrieve link.
type Kind string

const (
	KindSend    Kind = "send"
	KindRequest Kind = "request"
)

type ItemType string

const (
	TypeText ItemType = "text"
	TypeFile ItemType = "file"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrPending   = errors.New("not yet fulfilled")
	ErrFulfilled = errors.New("already submitted")
	ErrTooLarge  = errors.New("too large")
	ErrFull      = errors.New("server at capacity")
)

// Item is one named thing being asked for. Spec fields come from the creator;
// Text and Files are filled in by the submitter.
type Item struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        ItemType `json:"type"`

	Text  string `json:"text,omitempty"`
	Files []File `json:"files,omitempty"`
}

type File struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	path  string
	token string // minted at first retrieval; the one-shot download key
}

type Entry struct {
	Kind        Kind
	Title       string
	Description string
	Items       []Item

	SubmitID   string
	RetrieveID string

	Fulfilled bool
	ExpiresAt time.Time

	// ConsumedAt is when the payload was first handed out. The entry lingers
	// for Limits.Linger afterwards so a client that drops the connection, or an
	// agent that retries, can ask again. Then it self-destructs.
	ConsumedAt time.Time
	minted     []Item

	bytes int64
}

func (e *Entry) deadline(linger time.Duration) time.Time {
	if e.ConsumedAt.IsZero() {
		return e.ExpiresAt
	}
	if d := e.ConsumedAt.Add(linger); d.Before(e.ExpiresAt) {
		return d
	}
	return e.ExpiresAt
}

type role int

const (
	roleSubmit role = iota
	roleRetrieve
)

type ref struct {
	entry *Entry
	role  role
}

type Limits struct {
	MaxTextBytes  int64
	MaxFileBytes  int64
	MaxFiles      int
	MaxTotalBytes int64
	MaxTTL        time.Duration
	// Linger is how long a retrieved entry stays readable before it destroys
	// itself. Burn-strictly-on-first-byte breaks any client that retries.
	Linger time.Duration
}

type Store struct {
	mu      sync.Mutex
	byID    map[string]ref
	byFile  map[string]File
	scratch string
	limits  Limits
	total   int64
}

func NewStore(scratch string, limits Limits) *Store {
	return &Store{
		byID:    make(map[string]ref),
		byFile:  make(map[string]File),
		scratch: scratch,
		limits:  limits,
	}
}

// NewID returns an xid-shaped identifier: 24 lowercase base32 characters.
//
// Deliberately NOT rs/xid. A real xid encodes a timestamp, machine ID, PID and
// a counter, so holding one link makes its neighbours guessable — on a service
// whose whole security model is "possession of the URL", that is an enumeration
// hole. Same shape, 120 bits of crypto/rand instead.
func NewID() string {
	b := make([]byte, 15)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failing is not a recoverable condition
	}
	return base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").
		WithPadding(base32.NoPadding).EncodeToString(b)
}

func (s *Store) Put(e *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[e.SubmitID] = ref{e, roleSubmit}
	s.byID[e.RetrieveID] = ref{e, roleRetrieve}
}

func (s *Store) Lookup(id string) (*Entry, role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok || time.Now().After(r.entry.deadline(s.limits.Linger)) {
		return nil, 0, ErrNotFound
	}
	return r.entry, r.role, nil
}

func (s *Store) Reserve(n int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.total+n > s.limits.MaxTotalBytes {
		return ErrFull
	}
	s.total += n
	return nil
}

func (s *Store) Release(n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total -= n
}

// Submit finalises a draft. Everything the submitter typed and uploaded is
// already on the entry; this only flips the flag.
func (s *Store) Submit(e *Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Fulfilled {
		return ErrFulfilled
	}
	e.Fulfilled = true
	return nil
}

// Retrieve hands back the payload. The first call starts the linger clock and
// mints one-shot download tokens; later calls inside the window return exactly
// the same thing, so a retry or a dropped connection is not a lost secret.
func (s *Store) Retrieve(e *Entry) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !e.Fulfilled {
		return nil, ErrPending
	}
	if e.ConsumedAt.IsZero() {
		e.ConsumedAt = time.Now()
		e.minted = make([]Item, len(e.Items))
		copy(e.minted, e.Items)
		for i := range e.minted {
			files := make([]File, len(e.minted[i].Files))
			copy(files, e.minted[i].Files)
			for j := range files {
				files[j].token = NewID()
				s.byFile[files[j].token] = files[j]
			}
			e.minted[i].Files = files
		}
	}
	return e.minted, nil
}

// TakeFile resolves a download token. Tokens survive until the entry itself is
// destroyed, for the same retry-safety reason as Retrieve.
func (s *Store) TakeFile(token string) (File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.byFile[token]
	if !ok {
		return File{}, ErrNotFound
	}
	return f, nil
}

// AddFile spools a drafted file onto an item.
func (s *Store) AddFile(e *Entry, idx int, f File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Items[idx].Files = append(e.Items[idx].Files, f)
	e.bytes += f.Size
}

// DropFile removes a drafted file and reclaims its bytes.
func (s *Store) DropFile(e *Entry, idx, n int) error {
	s.mu.Lock()
	if e.Fulfilled || idx < 0 || idx >= len(e.Items) || n < 0 || n >= len(e.Items[idx].Files) {
		s.mu.Unlock()
		return ErrNotFound
	}
	f := e.Items[idx].Files[n]
	e.Items[idx].Files = append(e.Items[idx].Files[:n], e.Items[idx].Files[n+1:]...)
	e.bytes -= f.Size
	s.total -= f.Size
	s.mu.Unlock()

	if f.path != "" {
		os.Remove(f.path)
	}
	return nil
}

// SetText replaces one item's text. This is the autosave path: the form PUTs
// each field as you type, so backgrounding Telegram costs nothing. Only the
// growth is charged, checked under the same lock that applies it.
func (s *Store) SetText(e *Entry, idx int, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delta := int64(len(text)) - int64(len(e.Items[idx].Text))
	if delta > 0 && s.total+delta > s.limits.MaxTotalBytes {
		return ErrFull
	}
	e.Items[idx].Text = text
	e.bytes += delta
	s.total += delta
	return nil
}

// destroy removes an entry, its tokens and its files. Caller must not hold mu.
func (s *Store) destroy(e *Entry) {
	s.mu.Lock()
	delete(s.byID, e.SubmitID)
	delete(s.byID, e.RetrieveID)
	var paths []string
	for _, it := range e.Items {
		for _, f := range it.Files {
			if f.path != "" {
				paths = append(paths, f.path)
			}
		}
	}
	for _, it := range e.minted {
		for _, f := range it.Files {
			delete(s.byFile, f.token)
		}
	}
	s.total -= e.bytes
	s.mu.Unlock()

	for _, p := range paths {
		os.Remove(p)
	}
}

// Sweep destroys everything past its deadline. Returns the count, which is what
// the self-check asserts on.
func (s *Store) Sweep(now time.Time) int {
	s.mu.Lock()
	seen := make(map[*Entry]bool)
	var dead []*Entry
	for _, r := range s.byID {
		if !seen[r.entry] && now.After(r.entry.deadline(s.limits.Linger)) {
			seen[r.entry] = true
			dead = append(dead, r.entry)
		}
	}
	s.mu.Unlock()

	for _, e := range dead {
		s.destroy(e)
	}
	return len(dead)
}

func (s *Store) Run(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			s.Sweep(now)
		}
	}
}

func (s *Store) scratchFile() (*os.File, error) {
	return os.CreateTemp(s.scratch, "charon-")
}
