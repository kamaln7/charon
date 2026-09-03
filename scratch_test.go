package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The point of encoding the expiry in the filename is that a sweep needs no
// in-memory state: after a crash, the name is the only thing left that says
// whether a file still matters.
func TestScratchSweepUsesEncodedExpiry(t *testing.T) {
	dir := t.TempDir()
	s := NewScratch(dir)
	now := time.Now()

	stale, err := s.Create(now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	stale.Close()
	fresh, err := s.Create(now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	fresh.Close()

	// A file we did not write must be left alone, not guessed about.
	foreign := filepath.Join(dir, "notours.txt")
	if err := os.WriteFile(foreign, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if n := s.Sweep(now); n != 1 {
		t.Fatalf("swept %d files, want 1", n)
	}
	if _, err := os.Stat(stale.Name()); !os.IsNotExist(err) {
		t.Error("expired file survived the sweep")
	}
	if _, err := os.Stat(fresh.Name()); err != nil {
		t.Errorf("unexpired file was swept: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("sweep deleted a file it did not create")
	}
}

// A fresh process must be able to collect the previous one's orphans with
// nothing but the directory to go on.
func TestScratchSweepSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	old := NewScratch(dir)
	f, err := old.Create(time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	if n := NewScratch(dir).Sweep(time.Now()); n != 1 {
		t.Fatalf("a restarted process swept %d orphans, want 1", n)
	}
}

func TestExpiryFromName(t *testing.T) {
	if _, ok := expiryFromName("nodash"); ok {
		t.Error("a name with no separator parsed as ours")
	}
	if _, ok := expiryFromName("notanumber-abc"); ok {
		t.Error("a non-numeric prefix parsed as an expiry")
	}
	got, ok := expiryFromName("1700000000-abcdef")
	if !ok || got.Unix() != 1700000000 {
		t.Errorf("expiryFromName = %v, %v; want 1700000000", got.Unix(), ok)
	}
}

func TestSafeFilename(t *testing.T) {
	// The client's filename is metadata; it must never steer the path.
	for in, want := range map[string]string{
		"key.pem":            "key.pem",
		"../../etc/passwd":   "passwd",
		"/absolute/path.txt": "path.txt",
		"":                   "file",
		"/":                  "file",
	} {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
