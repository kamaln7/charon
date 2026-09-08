package main

import (
	"testing"
	"time"

	"github.com/kamaln7/charon/internal/api"
)

func testLimits() Limits {
	return Limits{
		MaxTextBytes:  1 << 10,
		MaxFileBytes:  1 << 10,
		MaxFiles:      4,
		MaxSecrets:    4,
		MaxTotalBytes: 1 << 20,
		DefaultTTL:    time.Hour,
		MaxTTL:        time.Hour,
		Linger:        30 * time.Second,
	}
}

func timeNowPlusHour() time.Time { return time.Now().Add(time.Hour) }

func newTestEntry(s *Store, ttl time.Duration) *Entry {
	e := &Entry{
		Kind:       api.KindRequest,
		Title:      "t",
		Secrets:    []Secret{{Name: "one", Type: api.TypeText}},
		SubmitID:   NewID(),
		RetrieveID: NewID(),
		ExpiresAt:  time.Now().Add(ttl),
	}
	s.Put(e)
	return e
}

// The two tokens must not be interchangeable: holding the public link is the
// entire security boundary, so a submit token that also retrieves would hand
// the secret straight back to whoever was asked for it.
func TestTokenRolesAreDistinct(t *testing.T) {
	s := NewStore(testLimits())
	e := newTestEntry(s, time.Hour)

	if _, role, err := s.Lookup(e.SubmitID); err != nil || role != roleSubmit {
		t.Fatalf("submit token: role=%v err=%v", role, err)
	}
	if _, role, err := s.Lookup(e.RetrieveID); err != nil || role != roleRetrieve {
		t.Fatalf("retrieve token: role=%v err=%v", role, err)
	}
	if e.SubmitID == e.RetrieveID {
		t.Fatal("tokens are identical")
	}
}

func TestRetrieveRequiresSubmission(t *testing.T) {
	s := NewStore(testLimits())
	e := newTestEntry(s, time.Hour)

	if _, err := s.Retrieve(e); err != ErrPending {
		t.Fatalf("want ErrPending before submit, got %v", err)
	}
	if err := s.Submit(e); err != nil {
		t.Fatal(err)
	}
	if err := s.Submit(e); err != ErrFulfilled {
		t.Fatalf("want ErrFulfilled on double submit, got %v", err)
	}
	if _, err := s.Retrieve(e); err != nil {
		t.Fatalf("retrieve after submit: %v", err)
	}
}

// Retrieval must be idempotent inside the linger window and dead after it.
// This is the behaviour that lets an agent retry without losing the payload,
// and the reason the entry is not burned on the first byte.
func TestLingerThenSelfDestruct(t *testing.T) {
	s := NewStore(testLimits())
	e := newTestEntry(s, time.Hour)
	s.SetText(e, 0, "hunter2")
	s.Submit(e)

	first, err := s.Retrieve(e)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Retrieve(e)
	if err != nil {
		t.Fatalf("second retrieve inside linger window: %v", err)
	}
	if first[0].Text != "hunter2" || second[0].Text != "hunter2" {
		t.Fatalf("payload changed between reads: %q then %q", first[0].Text, second[0].Text)
	}

	// Still alive just before the window closes...
	if paths := s.Sweep(e.ConsumedAt.Add(29 * time.Second)); paths != nil {
		t.Fatalf("swept %v while still lingering", paths)
	}
	if _, _, err := s.Lookup(e.RetrieveID); err != nil {
		t.Fatalf("entry gone during linger: %v", err)
	}
	// ...and gone after.
	s.Sweep(e.ConsumedAt.Add(31 * time.Second))
	if _, _, err := s.Lookup(e.RetrieveID); err != ErrNotFound {
		t.Fatalf("want ErrNotFound after self-destruct, got %v", err)
	}
}

func TestTTLExpiryWithoutRetrieval(t *testing.T) {
	s := NewStore(testLimits())
	e := newTestEntry(s, time.Minute)

	s.Sweep(time.Now())
	if _, _, err := s.Lookup(e.RetrieveID); err != nil {
		t.Fatalf("live entry was swept: %v", err)
	}
	s.Sweep(e.ExpiresAt.Add(time.Second))
	if _, _, err := s.Lookup(e.RetrieveID); err != ErrNotFound {
		t.Fatalf("expired entry survived the sweep: %v", err)
	}
}

// Byte accounting has to survive the draft edit paths, or a long session of
// typing and deleting would leak the global ceiling away.
func TestTotalBytesAccounting(t *testing.T) {
	s := NewStore(testLimits())
	e := newTestEntry(s, time.Hour)

	s.SetText(e, 0, "12345")
	if s.total != 5 {
		t.Fatalf("after write: total=%d want 5", s.total)
	}
	s.SetText(e, 0, "1")
	if s.total != 1 {
		t.Fatalf("after shrink: total=%d want 1", s.total)
	}
	s.SetText(e, 0, "")
	if s.total != 0 {
		t.Fatalf("after clear: total=%d want 0", s.total)
	}

	s.SetText(e, 0, "abc")
	s.destroy(e)
	if s.total != 0 {
		t.Fatalf("after destroy: total=%d want 0", s.total)
	}
}

func TestSetTextRespectsCeiling(t *testing.T) {
	l := testLimits()
	l.MaxTotalBytes = 4
	s := NewStore(l)
	e := newTestEntry(s, time.Hour)

	if err := s.SetText(e, 0, "abcd"); err != nil {
		t.Fatalf("write at the limit: %v", err)
	}
	if err := s.SetText(e, 0, "abcde"); err != ErrFull {
		t.Fatalf("want ErrFull past the limit, got %v", err)
	}
	if e.Secrets[0].Text != "abcd" {
		t.Fatalf("rejected write mutated the item: %q", e.Secrets[0].Text)
	}
}

func TestParseTTL(t *testing.T) {
	l := Limits{DefaultTTL: 24 * time.Hour, MaxTTL: 7 * 24 * time.Hour}
	for in, want := range map[string]time.Duration{
		"":    24 * time.Hour, // the documented default
		"15m": 15 * time.Minute,
		"6h":  6 * time.Hour,
		"3d":  72 * time.Hour,
		"1w":  l.MaxTTL,
	} {
		got, err := parseTTL(in, l)
		if err != nil || got != want {
			t.Errorf("parseTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"2w", "8d", "0d", "-1h", "banana", "1y"} {
		if _, err := parseTTL(in, l); err == nil {
			t.Errorf("parseTTL(%q) accepted an invalid or over-cap value", in)
		}
	}

	// A ceiling below the default must clamp rather than hand out more time
	// than the operator allows.
	tight := Limits{DefaultTTL: 24 * time.Hour, MaxTTL: time.Hour}
	if got, _ := parseTTL("", tight); got != time.Hour {
		t.Errorf("default TTL under a tight ceiling = %v, want 1h", got)
	}
	if opts := tight.TTLOptions(); len(opts) != 2 || opts[1] != "1h" {
		t.Errorf("TTLOptions under a 1h ceiling = %v, want [15m 1h]", opts)
	}
}

// IDs are the only thing protecting an entry, so they must not be sequential.
func TestIDsAreUnguessable(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := NewID()
		if len(id) != 24 {
			t.Fatalf("id %q has length %d, want 24", id, len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id %q after %d draws", id, i)
		}
		seen[id] = true
	}
	// Two consecutive IDs from a counter-based scheme would share a long
	// prefix; random ones essentially never do.
	a, b := NewID(), NewID()
	common := 0
	for common < len(a) && a[common] == b[common] {
		common++
	}
	if common > 6 {
		t.Fatalf("ids %q and %q share a %d-character prefix; are they sequential?", a, b, common)
	}
}
