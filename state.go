package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State is the boot mode the workstation picks up via iPXE. It survives
// restarts of this container so a pending "work" boot is not lost.
type State struct {
	path string
	mu   sync.Mutex
	data stateFile
}

type stateFile struct {
	Mode    string    `json:"mode"`
	Updated time.Time `json:"updated"`
}

func newState(path, defaultMode string) *State {
	s := &State{path: path, data: stateFile{Mode: defaultMode, Updated: time.Now()}}
	if b, err := os.ReadFile(path); err == nil {
		var loaded stateFile
		if err := json.Unmarshal(b, &loaded); err == nil && (loaded.Mode == ModeAI || loaded.Mode == ModeWork) {
			s.data = loaded
		}
	}
	return s
}

func (s *State) Mode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Mode
}

func (s *State) Snapshot() (string, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Mode, s.data.Updated
}

func (s *State) Set(mode string) error {
	if mode != ModeAI && mode != ModeWork {
		return fmt.Errorf("unknown mode %q", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.Mode == mode {
		return nil
	}
	s.data = stateFile{Mode: mode, Updated: time.Now()}
	return s.persist()
}

// persist writes the state file atomically; callers hold s.mu.
func (s *State) persist() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
