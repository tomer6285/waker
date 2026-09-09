package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tomer/waker/pkg/config"
)

type HostState struct {
	LastSeen    time.Time     `json:"last_seen,omitempty"`
	LastWake    time.Time     `json:"last_wake,omitempty"`
	LastLatency time.Duration `json:"last_latency,omitempty"`
	LastStatus  string        `json:"last_status,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	path     string
	States   map[string]*HostState `json:"states"`
}

func DefaultStorePath() (string, error) {
	dir, err := config.DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

func LoadStore(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = DefaultStorePath()
		if err != nil {
			return nil, err
		}
	}

	s := &Store{
		path:   path,
		States: make(map[string]*HostState),
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}

	if err := json.Unmarshal(data, s); err != nil {
		// Non-fatal, reset
		return s, nil
	}
	if s.States == nil {
		s.States = make(map[string]*HostState)
	}

	return s, nil
}

func (s *Store) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.path, data, 0600)
}

func (s *Store) Get(name string) HostState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.States[name]; ok && st != nil {
		return *st
	}
	return HostState{}
}

func (s *Store) RecordSeen(name string, latency time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.States[name]
	if !ok || st == nil {
		st = &HostState{}
		s.States[name] = st
	}
	st.LastSeen = time.Now()
	st.LastLatency = latency
	st.LastStatus = "online"
}

func (s *Store) RecordWake(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.States[name]
	if !ok || st == nil {
		st = &HostState{}
		s.States[name] = st
	}
	st.LastWake = time.Now()
}

func (s *Store) RecordStatus(name string, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.States[name]
	if !ok || st == nil {
		st = &HostState{}
		s.States[name] = st
	}
	st.LastStatus = status
	if status == "online" {
		st.LastSeen = time.Now()
	}
}
