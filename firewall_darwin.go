//go:build darwin

package main

import "sync"

// firewallState on Darwin.
type firewallState struct {
	mu sync.Mutex
}

func cleanupFirewall() {}

func (e *Engine) applyFirewall() {}

func (e *Engine) firewallLoop() {
	<-e.quit
}

func (e *Engine) firewallInfo() string { return "not used on macOS" }
