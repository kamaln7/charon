package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	rulekit "github.com/qpoint-io/rulekit/v2"
)

type Config struct {
	Addr     string
	BaseURL  string
	Scratch  string
	Limits   Limits
	Telegram Telegram
	Callback Callback
}

func LoadConfig() (Config, error) {
	c := Config{
		Addr:    env("CHARON_ADDR", ":1337"),
		BaseURL: strings.TrimSuffix(env("CHARON_BASE_URL", "http://localhost:1337"), "/"),
		Scratch: env("CHARON_SCRATCH_DIR", ""),
		Limits: Limits{
			MaxTextBytes:  envInt("CHARON_MAX_TEXT_BYTES", 64<<10),
			MaxFileBytes:  envInt("CHARON_MAX_FILE_BYTES", 16<<20),
			MaxFiles:      int(envInt("CHARON_MAX_FILES", 20)),
			MaxSecrets:    int(envInt("CHARON_MAX_SECRETS", 20)),
			MaxTotalBytes: envInt("CHARON_MAX_TOTAL_BYTES", 256<<20),
		},
		Telegram: Telegram{
			BotToken: env("CHARON_TELEGRAM_BOT_TOKEN", ""),
			BotName:  env("CHARON_TELEGRAM_BOT_NAME", ""),
			AppName:  env("CHARON_TELEGRAM_APP_NAME", ""),
			Required: env("CHARON_TELEGRAM_REQUIRED", "") == "true",
		},
		Callback: Callback{
			Secret: env("CHARON_CALLBACK_SECRET", ""),
			Client: &http.Client{Timeout: 15 * time.Second},
		},
	}

	var err error
	if c.Limits.DefaultTTL, err = parseDuration(env("CHARON_DEFAULT_TTL", "24h")); err != nil {
		return c, fmt.Errorf("CHARON_DEFAULT_TTL: %w", err)
	}
	if c.Limits.MaxTTL, err = parseDuration(env("CHARON_MAX_TTL", "7d")); err != nil {
		return c, fmt.Errorf("CHARON_MAX_TTL: %w", err)
	}
	if c.Limits.Linger, err = parseDuration(env("CHARON_LINGER", "60s")); err != nil {
		return c, fmt.Errorf("CHARON_LINGER: %w", err)
	}

	// Callbacks stay off until an operator writes a rule. Parsing here rather
	// than per-request means a typo is a refusal to boot, not a surprise
	// rejection the first time an agent tries to use the feature.
	if rule := env("CHARON_CALLBACK_RULE", ""); rule != "" {
		if c.Callback.Rule, err = rulekit.Parse(rule); err != nil {
			return c, fmt.Errorf("CHARON_CALLBACK_RULE: %w", err)
		}
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

// envInt reads a plain integer. Suffixed sizes would be nicer, but every value
// here is set once in a Compose file and never typed again.
func envInt(key string, def int64) int64 {
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
