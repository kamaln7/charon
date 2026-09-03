package main

import "fmt"

// Telegram builds direct links into the Mini App. That is all it does: the
// page is an ordinary web page, and opening it inside Telegram is a
// convenience, not an authentication scheme.
type Telegram struct {
	BotName string
	AppName string
}

func (t Telegram) Configured() bool { return t.BotName != "" && t.AppName != "" }

// DirectLink opens the Mini App straight onto one entry. Telegram passes the
// startapp value through as initData.start_param, so a bot only has to send
// this as plain text — no inline-keyboard payload, which is what makes it easy
// for an agent to hand over.
func (t Telegram) DirectLink(id string) string {
	if !t.Configured() {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s/%s?startapp=%s", t.BotName, t.AppName, id)
}
