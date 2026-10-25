package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// appPathCache remembers where each application name was seen running, so the
// kill switch still works while the application is not running right now.
type appPathCache struct {
	mu   sync.Mutex
	path string
	m    map[string][]string
}

func loadAppPathCache(dir string) *appPathCache {
	c := &appPathCache{path: filepath.Join(dir, "apppaths.json"), m: map[string][]string{}}
	if b, err := os.ReadFile(c.path); err == nil {
		json.Unmarshal(b, &c.m)
	}
	return c
}

func (c *appPathCache) add(name, path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.m[name] {
		if p == path {
			return false
		}
	}
	c.m[name] = append(c.m[name], path)
	b, _ := json.MarshalIndent(c.m, "", "  ")
	os.WriteFile(c.path, b, 0o600)
	return true
}

func (c *appPathCache) get(name string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.m[name]...)
}
