package main

import "testing"

func TestLoadConfigRejectsBadInts(t *testing.T) {
	t.Setenv("CHARON_MAX_FILES", "nope")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("accepted CHARON_MAX_FILES=nope")
	}
	t.Setenv("CHARON_MAX_FILES", "")
	t.Setenv("CHARON_MAX_FILE_BYTES", "-1")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("accepted CHARON_MAX_FILE_BYTES=-1")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("CHARON_ADDR", "")
	t.Setenv("CHARON_BASE_URL", "")
	t.Setenv("CHARON_SCRATCH_DIR", "")
	t.Setenv("CHARON_MAX_TEXT_BYTES", "")
	t.Setenv("CHARON_MAX_FILE_BYTES", "")
	t.Setenv("CHARON_MAX_FILES", "")
	t.Setenv("CHARON_MAX_SECRETS", "")
	t.Setenv("CHARON_MAX_TOTAL_BYTES", "")
	t.Setenv("CHARON_DEFAULT_TTL", "")
	t.Setenv("CHARON_MAX_TTL", "")
	t.Setenv("CHARON_LINGER", "")
	t.Setenv("CHARON_CALLBACK_RULE", "")
	t.Setenv("CHARON_CALLBACK_SECRET", "")
	t.Setenv("CHARON_SECRET_KEY", "")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":1337" || c.BaseURL != "http://localhost:1337" || c.Limits.MaxFiles != 20 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.Callback.Enabled() {
		t.Fatal("callbacks enabled with no rule")
	}
	if len(c.SecretKey) != 32 {
		t.Fatalf("autogen CHARON_SECRET_KEY len = %d, want 32", len(c.SecretKey))
	}
}

func TestLoadConfigSecretKey(t *testing.T) {
	t.Setenv("CHARON_SECRET_KEY", "operator-secret")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if string(c.SecretKey) != "operator-secret" {
		t.Fatalf("SecretKey = %q", c.SecretKey)
	}
}
