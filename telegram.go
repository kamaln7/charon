package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

// Telegram holds everything needed to (a) mint direct links into the Mini App
// and (b) verify who is looking at it.
type Telegram struct {
	BotToken string
	BotName  string
	AppName  string
	// Allowed is a whitelist of Telegram user IDs. Empty means any Telegram
	// user who can open the link.
	Allowed []int64
	// Required rejects API calls that carry no valid initData at all.
	Required bool
}

func (t Telegram) Configured() bool {
	return t.BotToken != "" && t.BotName != "" && t.AppName != ""
}

// DirectLink is the t.me URL that opens the Mini App straight onto one entry.
// Telegram passes the startapp value through as initData.start_param, so a
// bot only has to send this as plain text — no inline-keyboard payload needed,
// which is what makes it easy for Hermes to hand over.
func (t Telegram) DirectLink(id string) string {
	if !t.Configured() {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s/%s?startapp=%s", t.BotName, t.AppName, id)
}

var errBadInitData = errors.New("invalid Telegram initData")

// VerifyInitData checks the signature Telegram's webview hands to the page and
// returns the authenticated user ID.
//
// The scheme: build a newline-joined, key-sorted string of every field except
// hash and signature, then HMAC it with a key that is itself the HMAC of the
// bot token under the literal "WebAppData".
func (t Telegram) VerifyInitData(raw string) (int64, error) {
	if t.BotToken == "" {
		return 0, errBadInitData
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return 0, errBadInitData
	}
	want := values.Get("hash")
	if want == "" {
		return 0, errBadInitData
	}

	pairs := make([]string, 0, len(values))
	for k, v := range values {
		// signature is Telegram's separate Ed25519 field and, like hash, is
		// excluded from the string being hashed.
		if k == "hash" || k == "signature" {
			continue
		}
		pairs = append(pairs, k+"="+v[0])
	}
	sort.Strings(pairs)

	secret := hmacSHA256([]byte("WebAppData"), []byte(t.BotToken))
	got := hmacSHA256(secret, []byte(strings.Join(pairs, "\n")))
	if !hmac.Equal([]byte(hex.EncodeToString(got)), []byte(want)) {
		return 0, errBadInitData
	}

	// A valid signature is forever, so without a freshness check a leaked
	// initData string would be a permanent credential.
	var authDate int64
	fmt.Sscanf(values.Get("auth_date"), "%d", &authDate)
	if authDate == 0 || time.Since(time.Unix(authDate, 0)) > 24*time.Hour {
		return 0, errBadInitData
	}

	var user struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(values.Get("user")), &user); err != nil || user.ID == 0 {
		return 0, errBadInitData
	}
	return user.ID, nil
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// authorized gates every API route. With no bot token configured the service is
// wide open by design — it is meant to sit on a trusted network. Once a token
// is set, a presented initData must be valid, and the allow-list is enforced.
func (a *api) authorized(w http.ResponseWriter, r *http.Request) bool {
	t := a.cfg.Telegram
	raw := r.Header.Get("X-Telegram-Init-Data")
	if raw == "" {
		if t.Required {
			fail(w, http.StatusUnauthorized, "Telegram authentication required")
			return false
		}
		return true
	}
	id, err := t.VerifyInitData(raw)
	if err != nil {
		fail(w, http.StatusUnauthorized, "invalid Telegram authentication")
		return false
	}
	if len(t.Allowed) > 0 && !slices.Contains(t.Allowed, id) {
		fail(w, http.StatusForbidden, "not authorised")
		return false
	}
	return true
}
