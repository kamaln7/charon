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

type SecretType string

const (
	TypeText SecretType = "text"
	TypeFile SecretType = "file"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrPending   = errors.New("not yet fulfilled")
	ErrFulfilled = errors.New("already submitted")
	ErrTooLarge  = errors.New("too large")
	ErrFull      = errors.New("server at capacity")
)

// Secret is one named thing being asked for. Spec fields come from the creator;
// Text and Files are filled in by the submitter.
type Secret struct {
	Name        string
	Description string
	Type        SecretType

	Text  string
	Files []File
}

type File struct {
	Name  string
	Size  int64
	path  string
	token string // minted at first retrieval; the one-shot download key
}

func (f File) Open() (*os.File, error) { return os.Open(f.path) }

type Entry struct {
	Kind        Kind
	Title       string
	Description string
	Secrets     []Secret

	SubmitID    string
	RetrieveID  string
	CallbackURL string

	Fulfilled bool
	ExpiresAt time.Time

	// ConsumedAt is when the payload was first handed out. The entry lingers
	// for Limits.Linger afterwards so a client that drops the connection, or an
	// agent that retries, can ask again. Then it self-destructs.
	ConsumedAt time.Time
	minted     []Secret

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

func (r role) String() string {
	if r == roleSubmit {
		return "submit"
	}
	return "retrieve"
}

type ref struct {
	entry *Entry
	role  role
}

type Limits struct {
	MaxTextBytes  int64
	MaxFileBytes  int64
	MaxFiles      int
	MaxSecrets    int
	MaxTotalBytes int64
	DefaultTTL    time.Duration
	MaxTTL        time.Duration
	// Linger is how long a retrieved entry stays readable before it destroys
	// itself. Burn-strictly-on-first-byte breaks any client that retries.
	Linger time.Duration
}

type Store struct {
	mu     sync.Mutex
	byID   map[string]ref
	byFile map[string]File
	limits Limits
	total  int64
}

func NewStore(limits Limits) *Store {
	return &Store{
		byID:   make(map[string]ref),
		byFile: make(map[string]File),
		limits: limits,
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
func (s *Store) Retrieve(e *Entry) ([]Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !e.Fulfilled {
		return nil, ErrPending
	}
	if e.ConsumedAt.IsZero() {
		e.ConsumedAt = time.Now()
		e.minted = make([]Secret, len(e.Secrets))
		copy(e.minted, e.Secrets)
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

// Unconsume reopens an entry whose delivery failed, so the retrieve link keeps
// working instead of the secret self-destructing into a webhook that was down.
func (s *Store) Unconsume(e *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.ConsumedAt = time.Time{}
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

// AddFile spools a drafted file onto a secret.
func (s *Store) AddFile(e *Entry, idx int, f File) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Secrets[idx].Files = append(e.Secrets[idx].Files, f)
	e.bytes += f.Size
}

// DropFile removes a drafted file and reclaims its bytes. It returns the path
// to delete rather than deleting it, so the store never touches the disk.
func (s *Store) DropFile(e *Entry, idx, n int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Fulfilled || idx < 0 || idx >= len(e.Secrets) || n < 0 || n >= len(e.Secrets[idx].Files) {
		return "", ErrNotFound
	}
	f := e.Secrets[idx].Files[n]
	e.Secrets[idx].Files = append(e.Secrets[idx].Files[:n], e.Secrets[idx].Files[n+1:]...)
	e.bytes -= f.Size
	s.total -= f.Size
	return f.path, nil
}

// CountFiles reports how many files an entry holds across all its secrets.
func (s *Store) CountFiles(e *Entry) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sec := range e.Secrets {
		n += len(sec.Files)
	}
	return n
}

// SetText replaces one secret's text. This is the autosave path: the form PUTs
// each field as you type, so backgrounding Telegram costs nothing. Only the
// growth is charged, checked under the same lock that applies it.
func (s *Store) SetText(e *Entry, idx int, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delta := int64(len(text)) - int64(len(e.Secrets[idx].Text))
	if delta > 0 && s.total+delta > s.limits.MaxTotalBytes {
		return ErrFull
	}
	e.Secrets[idx].Text = text
	e.bytes += delta
	s.total += delta
	return nil
}

// destroy removes an entry and its tokens, returning the file paths its caller
// should delete. The store owns memory; the scratch directory owns disk.
func (s *Store) destroy(e *Entry) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, e.SubmitID)
	delete(s.byID, e.RetrieveID)
	var paths []string
	for _, sec := range e.Secrets {
		for _, f := range sec.Files {
			if f.path != "" {
				paths = append(paths, f.path)
			}
		}
	}
	for _, sec := range e.minted {
		for _, f := range sec.Files {
			delete(s.byFile, f.token)
		}
	}
	s.total -= e.bytes
	return paths
}

// Sweep destroys everything past its deadline, returning the file paths that
// belonged to it. Returns nil when nothing expired.
func (s *Store) Sweep(now time.Time) []string {
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

	var paths []string
	for _, e := range dead {
		paths = append(paths, s.destroy(e)...)
	}
	return paths
}
