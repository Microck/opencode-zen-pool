package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

type keyState struct {
	ExhaustedUntil   time.Time      `json:"cooldown_until,omitempty"`
	SuspendedUntil   time.Time      `json:"suspended_until,omitempty"`
	RateLimitedUntil time.Time      `json:"rate_limited_until,omitempty"`
	TransientUntil   time.Time      `json:"transient_until,omitempty"`
	IgnoreBefore     time.Time      `json:"ignore_results_before,omitempty"`
	LastFailure      classification `json:"last_failure,omitempty"`
	LastFailureAt    time.Time      `json:"last_failure_at,omitempty"`
	LastSuccessAt    time.Time      `json:"last_success_at,omitempty"`
	LastAction       string         `json:"last_action,omitempty"`
	Success          uint64         `json:"success"`
	Failed           uint64         `json:"failed"`
}
type persistedState struct {
	Version int                  `json:"version"`
	Current string               `json:"current,omitempty"`
	Cursor  string               `json:"cursor,omitempty"`
	Keys    map[string]*keyState `json:"keys"`
}

func emptyState() persistedState { return persistedState{Version: 1, Keys: map[string]*keyState{}} }

var hashIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var errState = errors.New("Zen pool state is unavailable or invalid (details withheld)")

type stateStore interface {
	Load() (persistedState, error)
	Save(persistedState) error
	Close() error
}

// memoryStore is a real in-memory JSON store, useful for deterministic tests.
// It preserves serialization semantics rather than sharing mutable pointers.
type memoryStore struct {
	mu  sync.Mutex
	raw []byte
}

func (s *memoryStore) Load() (persistedState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.raw) == 0 {
		return emptyState(), nil
	}
	return decodeState(s.raw)
}
func (s *memoryStore) Save(v persistedState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(v)
	if err == nil {
		s.raw = b
	}
	return err
}
func (*memoryStore) Close() error { return nil }

type fileStore struct {
	path string
	lock *os.File
}

func newFileStore(dir string) (*fileStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errState
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errState
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("Zen pool state is already owned by another process")
	}
	return &fileStore{path: filepath.Join(dir, "state.json"), lock: lock}, nil
}
func decodeState(b []byte) (persistedState, error) {
	s := emptyState()
	if err := json.Unmarshal(b, &s); err != nil {
		return persistedState{}, errState
	}
	// Missing optional fields are left at their defaults. A missing version is
	// not a historical-format shim: this is the sole schema and its default is 1.
	if s.Version != 1 || s.Keys == nil || len(s.Keys) > 10000 {
		return persistedState{}, errState
	}
	for _, id := range []string{s.Current, s.Cursor} {
		if id != "" && !hashIDPattern.MatchString(id) {
			return persistedState{}, errState
		}
	}
	for id, k := range s.Keys {
		if !hashIDPattern.MatchString(id) || k == nil {
			return persistedState{}, errState
		}
	}
	return s, nil
}
func (s *fileStore) Load() (persistedState, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return emptyState(), nil
	}
	if err != nil {
		return persistedState{}, errState
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil || len(b) > 8<<20 {
		return persistedState{}, errState
	}
	return decodeState(b)
}
func (s *fileStore) Save(v persistedState) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errState
	}
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return errState
	}
	name := tmp.Name()
	defer os.Remove(name)
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
		}
	}()
	if err = tmp.Chmod(0600); err != nil {
		return errState
	}
	if _, err = tmp.Write(append(b, '\n')); err != nil {
		return errState
	}
	if err = tmp.Sync(); err != nil {
		return errState
	}
	if err = tmp.Close(); err != nil {
		return errState
	}
	ok = true
	if err = os.Rename(name, s.path); err != nil {
		return errState
	}
	d, err := os.Open(dir)
	if err != nil {
		return errState
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return errState
	}
	return nil
}
func (s *fileStore) Close() error {
	if s.lock == nil {
		return nil
	}
	e := s.lock.Close()
	s.lock = nil
	return e
}
