package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Server is one stored Hysteria2 profile. Link holds the secret and is never
// returned through the API.
type Server struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Link string `json:"link"`
}

// ServerView is the secret-free representation shown in the UI.
type ServerView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Host   string `json:"host"`
	Port   string `json:"port"`
	SNI    string `json:"sni"`
	Active bool   `json:"active"`
	Obfs   string `json:"obfs,omitempty"` // "salamander" / "gecko"
	Hop    string `json:"hop,omitempty"`  // port hopping set, e.g. "20000-30000"
}

type storeData struct {
	Servers     []Server `json:"servers"`
	Last        string   `json:"last"`
	AutoConnect bool     `json:"auto_connect"`
	Lang        string   `json:"lang,omitempty"` // "en" (default) or "ru"
}

type serverStore struct {
	mu       sync.Mutex
	path     string
	secure   bool // restrict file permissions (installed mode)
	d        storeData
	loadedOK bool
}

func newServerStore(dir string, secure bool) *serverStore {
	s := &serverStore{path: filepath.Join(dir, "servers.json"), secure: secure}
	if b, err := os.ReadFile(s.path); err == nil {
		if json.Unmarshal(b, &s.d) == nil {
			s.loadedOK = true
		}
	}
	return s
}

func (s *serverStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.d, "", "  ")
	if err := os.WriteFile(s.path, b, 0o600); err != nil {
		return err
	}
	if s.secure {
		lockDownFile(s.path, false)
	}
	return nil
}

func randomID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// add parses and stores a link. The display name comes from the #fragment.
func (s *serverStore) add(link, name string) (Server, error) {
	link = strings.TrimSpace(link)
	l, err := parseLink(link)
	if err != nil {
		return Server{}, err
	}
	if l.Auth == "" {
		return Server{}, errors.New(tr("в ссылке нет пароля (hy2://пароль@хост:порт)", "the link has no password (hy2://password@host:port)"))
	}
	if name == "" {
		if l.Name != "" {
			name = l.Name
		} else {
			name = l.Host
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.d.Servers {
		if e.Link == link {
			return e, nil // already there
		}
	}
	srv := Server{ID: randomID(), Name: name, Link: link}
	s.d.Servers = append(s.d.Servers, srv)
	if s.d.Last == "" {
		s.d.Last = srv.ID
	}
	return srv, s.saveLocked()
}

func (s *serverStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.d.Servers[:0]
	for _, e := range s.d.Servers {
		if e.ID != id {
			out = append(out, e)
		}
	}
	s.d.Servers = out
	if s.d.Last == id {
		s.d.Last = ""
		if len(out) > 0 {
			s.d.Last = out[0].ID
		}
	}
	return s.saveLocked()
}

func (s *serverStore) get(id string) (Server, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" {
		id = s.d.Last
	}
	for _, e := range s.d.Servers {
		if e.ID == id {
			return e, true
		}
	}
	if id == "" && len(s.d.Servers) > 0 {
		return s.d.Servers[0], true
	}
	return Server{}, false
}

// getExact looks a profile up by id only (no "last used" fallback).
func (s *serverStore) getExact(id string) (Server, bool) {
	if id == "" {
		return Server{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.d.Servers {
		if e.ID == id {
			return e, true
		}
	}
	return Server{}, false
}

func (s *serverStore) setLast(id string) {
	s.mu.Lock()
	s.d.Last = id
	s.saveLocked()
	s.mu.Unlock()
}

func (s *serverStore) setAutoConnect(on bool) {
	s.mu.Lock()
	s.d.AutoConnect = on
	s.saveLocked()
	s.mu.Unlock()
}

func (s *serverStore) getLang() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.Lang
}

func (s *serverStore) setLangPref(l string) {
	s.mu.Lock()
	s.d.Lang = l
	s.saveLocked()
	s.mu.Unlock()
}

func (s *serverStore) autoConnect() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.d.AutoConnect
}

func (s *serverStore) views(active string) []ServerView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ServerView, 0, len(s.d.Servers))
	for _, e := range s.d.Servers {
		v := ServerView{ID: e.ID, Name: e.Name, Active: e.ID == active}
		if l, err := parseLink(e.Link); err == nil {
			v.Host, v.Port, v.SNI = l.Host, l.Port, l.SNI
			v.Obfs, v.Hop = l.Obfs, l.Ports
		}
		out = append(out, v)
	}
	return out
}

func (s *serverStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.d.Servers)
}
