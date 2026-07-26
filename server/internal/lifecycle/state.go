// Package lifecycle exposes the process drain state shared by readiness and
// work admission. Draining is one-way for the lifetime of a process.
package lifecycle

import (
	"sync"
)

type State struct {
	mu           sync.Mutex
	draining     bool
	probesClosed bool
	active       sync.WaitGroup
}

func (s *State) Drain() {
	s.mu.Lock()
	s.draining = true
	s.mu.Unlock()
}

func (s *State) IsDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

// BeginRequest atomically admits work only while the process is accepting new
// requests. This keeps WaitGroup.Add from racing with Drain followed by Wait.
func (s *State) BeginRequest() (func(), bool) {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return nil, false
	}
	s.active.Add(1)
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(s.active.Done) }, true
}

// BeginProbe tracks health/readiness handlers while continuing to admit them
// during the graceful drain. StopProbes closes this admission gate before the
// listener is force-closed and Wait begins, preventing WaitGroup Add/Wait races.
func (s *State) BeginProbe() (func(), bool) {
	s.mu.Lock()
	if s.probesClosed {
		s.mu.Unlock()
		return nil, false
	}
	s.active.Add(1)
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(s.active.Done) }, true
}

func (s *State) StopProbes() {
	s.mu.Lock()
	s.probesClosed = true
	s.mu.Unlock()
}

func (s *State) Wait() {
	s.active.Wait()
}
