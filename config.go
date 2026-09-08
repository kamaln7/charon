package main

import (
	"crypto/rand"
	"fmt"
	"os"
	"strconv"
	"strings"

	rulekit "github.com/qpoint-io/rulekit/v2"
)

type Config struct {
	Addr      string
	BaseURL   string
	Scratch   string
	SecretKey []byte
	Limits    Limits
	Callback  Callback
}

func LoadConfig() (Config, error) {
	c := Config{
		Addr:    env("CHARON_ADDR", ":1337"),
		BaseURL: strings.TrimSuffix(env("CHARON_BASE_URL", "http://localhost:1337"), "/"),
		Scratch: env("CHARON_SCRATCH_DIR", ""),
		Callback: Callback{
			Secret: env("CHARON_CALLBACK_SECRET", ""),
			Client: callbackClient(),
		},
	}

	var err error
	if c.Limits.MaxTextBytes, err = envInt("CHARON_MAX_TEXT_BYTES", 64<<10); err != nil {
		return c, err
	}
	if c.Limits.MaxFileBytes, err = envInt("CHARON_MAX_FILE_BYTES", 16<<20); err != nil {
		return c, err
	}
	var n int64
	if n, err = envInt("CHARON_MAX_FILES", 20); err != nil {
		return c, err
	}
	c.Limits.MaxFiles = int(n)
	if n, err = envInt("CHARON_MAX_SECRETS", 20); err != nil {
		return c, err
	}
	c.Limits.MaxSecrets = int(n)
	if c.Limits.MaxTotalBytes, err = envInt("CHARON_MAX_TOTAL_BYTES", 256<<20); err != nil {
		return c, err
	}
	if c.Limits.DefaultTTL, err = parseDuration(env("CHARON_DEFAULT_TTL", "24h")); err != nil {
		return c, fmt.Errorf("CHARON_DEFAULT_TTL: %w", err)
	}
	if c.Limits.MaxTTL, err = parseDuration(env("CHARON_MAX_TTL", "7d")); err != nil {
		return c, fmt.Errorf("CHARON_MAX_TTL: %w", err)
	}
	if c.Limits.Linger, err = parseNonNegativeDuration(env("CHARON_LINGER", "60s")); err != nil {
		return c, fmt.Errorf("CHARON_LINGER: %w", err)
	}

	if v := os.Getenv("CHARON_SECRET_KEY"); v != "" {
		c.SecretKey = []byte(v)
	} else {
		c.SecretKey = make([]byte, 32)
		if _, err := rand.Read(c.SecretKey); err != nil {
			return c, fmt.Errorf("CHARON_SECRET_KEY: %w", err)
		}
	}

	// Callbacks stay off until an operator writes a rule. Parsing here rather
	// than per-request means a typo is a refusal to boot, not a surprise
	// rejection the first time an agent tries to use the feature.
	if rule := env("CHARON_CALLBACK_RULE", ""); rule != "" {
		if c.Callback.Rule, err = rulekit.Parse(rule); err != nil {
			return c, fmt.Errorf("CHARON_CALLBACK_RULE: %w", err)
		}
	}

	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) (int64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: want a positive integer, got %q", key, v)
	}
	return n, nil
}
