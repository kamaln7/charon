package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr     string
	BaseURL  string
	Scratch  string
	Limits   Limits
	Telegram Telegram
}

func LoadConfig() (Config, error) {
	c := Config{
		Addr:    env("CHARON_ADDR", ":1337"),
		BaseURL: strings.TrimSuffix(env("CHARON_BASE_URL", "http://localhost:1337"), "/"),
		Scratch: env("CHARON_SCRATCH_DIR", ""),
		Limits: Limits{
			MaxTextBytes:  envBytes("CHARON_MAX_TEXT_BYTES", 64<<10),
			MaxFileBytes:  envBytes("CHARON_MAX_FILE_BYTES", 16<<20),
			MaxFiles:      int(envBytes("CHARON_MAX_FILES", 20)),
			MaxTotalBytes: envBytes("CHARON_MAX_TOTAL_BYTES", 256<<20),
		},
		Telegram: Telegram{
			BotToken: env("CHARON_TELEGRAM_BOT_TOKEN", ""),
			BotName:  env("CHARON_TELEGRAM_BOT_NAME", ""),
			AppName:  env("CHARON_TELEGRAM_APP_NAME", ""),
			Required: env("CHARON_TELEGRAM_REQUIRED", "") == "true",
		},
	}

	var err error
	if c.Limits.MaxTTL, err = time.ParseDuration(env("CHARON_MAX_TTL", "168h")); err != nil {
		return c, fmt.Errorf("CHARON_MAX_TTL: %w", err)
	}
	if c.Limits.Linger, err = time.ParseDuration(env("CHARON_LINGER", "60s")); err != nil {
		return c, fmt.Errorf("CHARON_LINGER: %w", err)
	}
	for _, id := range strings.Split(env("CHARON_TELEGRAM_ALLOWED_USERS", ""), ",") {
		if id = strings.TrimSpace(id); id != "" {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return c, fmt.Errorf("CHARON_TELEGRAM_ALLOWED_USERS: %q is not a user ID", id)
			}
			c.Telegram.Allowed = append(c.Telegram.Allowed, n)
		}
	}
	if c.Telegram.Required && c.Telegram.BotToken == "" {
		return c, fmt.Errorf("CHARON_TELEGRAM_REQUIRED needs CHARON_TELEGRAM_BOT_TOKEN")
	}
	return c, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBytes reads a plain integer. Suffixed sizes would be nicer, but every
// value here is set once in a Compose file and never typed again.
func envBytes(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
