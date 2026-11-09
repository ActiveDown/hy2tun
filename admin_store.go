package main

// ssh-admin.json in the data directory keeps the SSH access of deployed
// (or manually attached) servers for the admin panel: host, port, username,
// password, and the Traffic Stats API endpoint of the server. The file is
// readable only by the service (0600 + lockDownFile in the installed mode);
// the API never returns the credential as a whole and never logs it.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type adminCredential struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	StatsPort   int    `json:"stats_port,omitempty"`
	StatsSecret string `json:"stats_secret,omitempty"`
	SavedAt     int64  `json:"saved_at"`
}

type adminStore struct {
	mu     sync.Mutex
	path   string
	secure bool
	creds  map[string]adminCredential
}

func newAdminStore(dir string, secure bool) *adminStore {
	s := &adminStore{path: filepath.Join(dir, "ssh-admin.json"), secure: secure, creds: map[string]adminCredential{}}
	if b, err := os.ReadFile(s.path); err == nil {
		if json.Unmarshal(b, &s.creds) != nil {
			s.creds = map[string]adminCredential{} // corrupt file: nothing stored
		}
	}
	return s
}

// saveLocked replaces the file atomically, the way ssh-host-keys.json is saved.
func (s *adminStore) saveLocked() error {
	b, err := json.MarshalIndent(s.creds, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".ssh-admin-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), s.path); err != nil {
		return err
	}
	if s.secure {
		lockDownFile(s.path, false)
	}
	return nil
}

func (s *adminStore) get(id string) (adminCredential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.creds[id]
	return c, ok
}

func (s *adminStore) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.creds[id]
	return ok
}

func (s *adminStore) set(id string, c adminCredential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.SavedAt = time.Now().Unix()
	s.creds[id] = c
	return s.saveLocked()
}

func (s *adminStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.creds[id]; !ok {
		return nil
	}
	delete(s.creds, id)
	return s.saveLocked()
}
