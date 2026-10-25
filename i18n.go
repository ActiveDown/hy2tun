package main

import (
	"strings"
	"sync/atomic"
)

// The application speaks English and Russian. English is always the default;
// Russian is used only after the user picks it in the window (the choice is
// saved in servers.json and shared by the service, the tray and the window).

var curLang atomic.Value // "en" | "ru"

func init() { curLang.Store("en") }

func setLang(l string) {
	if l = normalizeLang(l); l != "" {
		curLang.Store(l)
	}
}

func lang() string { return curLang.Load().(string) }

// tr picks the Russian or the English variant of a message.
func tr(ru, en string) string {
	if lang() == "ru" {
		return ru
	}
	return en
}

func normalizeLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	switch {
	case strings.HasPrefix(l, "ru"):
		return "ru"
	case strings.HasPrefix(l, "en"):
		return "en"
	}
	return ""
}
