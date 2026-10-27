package main

// firewallState: the kill switch on Android is the system "Always-on VPN" with
// "Block connections without VPN".
type firewallState struct{}

func cleanupFirewall() {}

func (e *Engine) applyFirewall() {}

func (e *Engine) firewallLoop() {
	<-e.quit
}

func (e *Engine) firewallInfo() string { return "not used on Android" }
