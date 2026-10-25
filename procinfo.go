package main

// procRef identifies one process (or an ancestor of it).
type procRef struct {
	Name string `json:"name"` // lower-case base name, e.g. "chrome" or "chrome.exe"
	Path string `json:"path"` // lower-case full path when it could be read
}

// procInfo is the owner of a connection plus its ancestor chain (self first).
type procInfo struct {
	PID   uint32
	Chain []procRef
}

func (p *procInfo) name() string {
	if p == nil || len(p.Chain) == 0 {
		return ""
	}
	return p.Chain[0].Name
}

// runningProc is one entry of the "pick an app" list in the panel.
type runningProc struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Conns int    `json:"conns"`
}
