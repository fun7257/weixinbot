package session

import (
	"context"
	"fmt"
	"sync"
)

// Manager runs multiple account Sessions concurrently.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*managed
}

type managed struct {
	sess   *Session
	cancel context.CancelFunc
	done   chan error
}

// NewManager creates an empty SessionManager.
func NewManager() *Manager {
	return &Manager{sessions: make(map[string]*managed)}
}

// Start begins Session.Run in a background goroutine for the account.
// If already running, returns an error.
// The goroutine always recovers panics and signals done so Stop cannot hang.
func (m *Manager) Start(parent context.Context, sess *Session) error {
	if sess == nil {
		return fmt.Errorf("session: nil session")
	}
	id := sess.AccountID()
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; ok {
		return fmt.Errorf("session: account %s already started", id)
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan error, 1)
	m.sessions[id] = &managed{sess: sess, cancel: cancel, done: done}
	go func() {
		var runErr error
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("session: panic in Run: %v", r)
			}
			done <- runErr
		}()
		runErr = sess.Run(ctx)
	}()
	return nil
}

// Stop cancels one account's Run and waits for exit.
func (m *Manager) Stop(accountID string) error {
	m.mu.Lock()
	mg, ok := m.sessions[accountID]
	if ok {
		delete(m.sessions, accountID)
	}
	m.mu.Unlock()
	if !ok {
		return nil
	}
	mg.cancel()
	<-mg.done
	return nil
}

// StopAll stops every running session.
func (m *Manager) StopAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Stop(id)
	}
}

// Get returns a running session by account id.
func (m *Manager) Get(accountID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mg, ok := m.sessions[accountID]
	if !ok {
		return nil, false
	}
	return mg.sess, true
}

// List returns account IDs currently managed.
func (m *Manager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		out = append(out, id)
	}
	return out
}
