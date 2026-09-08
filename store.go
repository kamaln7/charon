package main

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kamaln7/charon/internal/api"
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
	Type        api.SecretType

	Text  string
	Files []File
}

type File struct {
	Name  string
	Size  int64
	path  string
	token string // minted at first retrieval; the one-shot download key
}

type Entry struct {
	Kind        api.Kind
	Title       string
	Description string
	Secrets     []Secret

	SubmitID    string
	RetrieveID  string
	ManageID    string
	CallbackURL string

	Fulfilled bool
	ExpiresAt time.Time
	// Linger is how long the payload stays readable after the first retrieve.
	// Zero means the next lookup misses; Sweep then deletes the entry and files.
	Linger time.Duration

	// ConsumedAt is when the payload was first handed out. The entry lingers
	// for Linger afterwards so a client that drops the connection, or an
	// agent that retries, can ask again. Then it self-destructs.
	ConsumedAt time.Time
	minted     []Secret

	bytes int64

	// done is closed when there is nothing left to wait for: the entry was
	// submitted, or it was destroyed. Long-polling viewers block on it.
	done chan struct{}
}

// finish closes done exactly once. Callers hold the store lock.
func (e *Entry) finish() {
	if e.done != nil {
		close(e.done)
		e.done = nil
	}
}

func (e *Entry) deadline() time.Time {
	if e.ConsumedAt.IsZero() {
		return e.ExpiresAt
	}
	if d := e.ConsumedAt.Add(e.Linger); d.Before(e.ExpiresAt) {
		return d
	}
	return e.ExpiresAt
}

type role int

const (
	roleSubmit role = iota
	roleRetrieve
	// roleManage is the creator's own token. It reads back the other two links
	// and can destroy the entry. It cannot submit a draft or consume the
	// payload, so the manage page never has to be trusted with the secret.
	roleManage
)

func (r role) String() string {
	switch r {
	case roleSubmit:
		return "submit"
	case roleRetrieve:
		return "retrieve"
	default:
		return "manage"
	}
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
	// Linger is the maximum post-retrieve window a create may request.
	// The per-entry default is zero.
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
	if e.done == nil {
		e.done = make(chan struct{})
	}
	s.byID[e.SubmitID] = ref{e, roleSubmit}
	s.byID[e.RetrieveID] = ref{e, roleRetrieve}
	s.byID[e.ManageID] = ref{e, roleManage}
}

func (s *Store) Lookup(id string) (*Entry, role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok || time.Now().After(r.entry.deadline()) {
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
	if n == 0 {
		return
	}
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
	e.finish()
	return nil
}

// Done reports when waiting on an entry is pointless: it fires on submission
// and on destruction, so a viewer blocked on it wakes either way. An entry
// that is already finished yields a closed channel.
func (s *Store) Done(e *Entry) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.done == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return e.done
}

// view runs a read of the entry under the store lock, so a response is never
// built while a submission is landing on the same fields.
func (s *Store) view(e *Entry, read func() api.EntryResponse) api.EntryResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	return read()
}

func (s *Store) mintLocked(e *Entry) {
	if e.minted != nil {
		return
	}
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

// snapshot is the payload without starting linger. Callbacks use this so a
// failed webhook does not consume the retrieve link.
func (s *Store) snapshot(e *Entry) ([]Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !e.Fulfilled {
		return nil, ErrPending
	}
	s.mintLocked(e)
	return e.minted, nil
}

// Retrieve hands back the payload. The first call starts the linger clock and
// mints download tokens; later calls inside the window return exactly the
// same thing, so a retry or a dropped connection is not a lost secret.
func (s *Store) Retrieve(e *Entry) ([]Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !e.Fulfilled {
		return nil, ErrPending
	}
	s.mintLocked(e)
	if e.ConsumedAt.IsZero() {
		e.ConsumedAt = time.Now()
	}
	return e.minted, nil
}

// Unconsume drops a snapshot that was never retrieved, so a failed webhook
// does not start linger. If someone already retrieved, this is a no-op.
func (s *Store) Unconsume(e *Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !e.ConsumedAt.IsZero() {
		return
	}
	for _, sec := range e.minted {
		for _, f := range sec.Files {
			delete(s.byFile, f.token)
		}
	}
	e.minted = nil
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
func (s *Store) AddFile(e *Entry, idx int, f File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[e.SubmitID]; !ok {
		return ErrNotFound
	}
	if e.Fulfilled {
		return ErrFulfilled
	}
	n := 0
	for _, sec := range e.Secrets {
		n += len(sec.Files)
	}
	if n >= s.limits.MaxFiles {
		return fmt.Errorf("at most %d files", s.limits.MaxFiles)
	}
	e.Secrets[idx].Files = append(e.Secrets[idx].Files, f)
	e.bytes += f.Size
	return nil
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
// each field as you type, so backgrounding the submit page costs nothing. Only
// the growth is charged, checked under the same lock that applies it.
func (s *Store) SetText(e *Entry, idx int, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.Fulfilled {
		return ErrFulfilled
	}
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
	if _, ok := s.byID[e.SubmitID]; !ok {
		return nil
	}
	delete(s.byID, e.SubmitID)
	delete(s.byID, e.RetrieveID)
	delete(s.byID, e.ManageID)
	e.finish()
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
		if !seen[r.entry] && now.After(r.entry.deadline()) {
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
