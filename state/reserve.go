package state

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Typed policy errors for errors.Is mapping in session.
var (
	// ErrSessionWindow indicates missing token or expired inbound window.
	ErrSessionWindow = errors.New("state: session window closed")
	// ErrOutboundQuota indicates outbound count would exceed the hard quota.
	ErrOutboundQuota = errors.New("state: outbound quota exceeded")
)

// ReserveOpts configures atomic outbound reservation against peer state.
type ReserveOpts struct {
	// N is number of outbound slots to reserve (must be >= 1).
	N int
	// SessionWindow max age of last inbound (0 = skip time check only if DisableSessionWindow).
	SessionWindow time.Duration
	// OutboundQuota max outbound since last inbound.
	OutboundQuota int
	// Now clock.
	Now time.Time
	// DisableSessionWindow skips the time check.
	DisableSessionWindow bool
	// DisableOutboundQuota skips the count check.
	DisableOutboundQuota bool
	// AllowMissingToken allows empty context token.
	AllowMissingToken bool
}

// ReserveResult is returned by TryReserveOutbound.
type ReserveResult struct {
	// ContextToken is the single source of truth for the wire payload.
	ContextToken string
	// OutboundCount is the count after reservation.
	OutboundCount int
}

// ErrSessionWindow and ErrOutboundQuota are duplicated as strings in session package
// via errors.Is matching — state returns plain errors that session maps.
// Callers should use session package sentinels; state returns descriptive errors.

// TryReserveOutbound atomically checks policy and increments OutboundCount by N.
// Holds the store mutex for the entire check+persist so concurrent Send* cannot
// overshoot the hard quota.
func (s *Store) TryReserveOutbound(accountID, userID string, opts ReserveOpts) (ReserveResult, error) {
	if opts.N <= 0 {
		opts.N = 1
	}
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return ReserveResult{}, err
	}
	if userID == "" {
		return ReserveResult{}, fmt.Errorf("state: empty userID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ensure token map loaded
	if s.tokens[safe] == nil {
		s.tokens[safe] = make(map[string]string)
		if disk, err := s.readContextTokensLocked(safe); err == nil {
			for k, v := range disk {
				s.tokens[safe][k] = v
			}
		}
	}

	m, err := s.readPeerStatesLocked(safe)
	if err != nil && !os.IsNotExist(err) {
		return ReserveResult{}, err
	}
	if m == nil {
		m = make(map[string]PeerState)
	}
	ps := m[userID]

	// Single token resolution: legacy map first, then peer-state.
	token := s.tokens[safe][userID]
	if token == "" {
		token = ps.ContextToken
	}
	// Keep maps aligned when we have a token from either side.
	if token != "" {
		s.tokens[safe][userID] = token
		ps.ContextToken = token
	}

	if token == "" && !opts.AllowMissingToken {
		return ReserveResult{}, fmt.Errorf("%w: missing context_token", ErrSessionWindow)
	}
	if !opts.DisableSessionWindow {
		if ps.LastInboundAt.IsZero() {
			return ReserveResult{}, fmt.Errorf("%w: no inbound yet", ErrSessionWindow)
		}
		win := opts.SessionWindow
		if win <= 0 {
			win = 24 * time.Hour
		}
		now := opts.Now
		if now.IsZero() {
			now = time.Now()
		}
		if now.Sub(ps.LastInboundAt) > win {
			return ReserveResult{}, fmt.Errorf("%w: last inbound too old", ErrSessionWindow)
		}
	}
	if !opts.DisableOutboundQuota {
		quota := opts.OutboundQuota
		if quota <= 0 {
			quota = 10
		}
		if ps.OutboundCount+opts.N > quota {
			return ReserveResult{}, fmt.Errorf("%w: need %d have room for %d", ErrOutboundQuota, opts.N, quota-ps.OutboundCount)
		}
	}

	ps.OutboundCount += opts.N
	m[userID] = ps
	if err := s.writePeerStatesLocked(safe, m); err != nil {
		return ReserveResult{}, fmt.Errorf("state: reserve persist: %w", err)
	}
	// Sync token file if we filled from peer
	if token != "" {
		if err := s.writeContextTokensLocked(safe); err != nil {
			// roll back count? Prefer fail closed after partial — try reverse count
			ps.OutboundCount -= opts.N
			if ps.OutboundCount < 0 {
				ps.OutboundCount = 0
			}
			m[userID] = ps
			_ = s.writePeerStatesLocked(safe, m)
			return ReserveResult{}, fmt.Errorf("state: token persist: %w", err)
		}
	}
	return ReserveResult{ContextToken: token, OutboundCount: ps.OutboundCount}, nil
}

// ReleaseOutbound decrements reserved slots after a failed send (best-effort).
func (s *Store) ReleaseOutbound(accountID, userID string, n int) error {
	if n <= 0 {
		return nil
	}
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return err
	}
	if userID == "" {
		return fmt.Errorf("state: empty userID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readPeerStatesLocked(safe)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if m == nil {
		return nil
	}
	ps := m[userID]
	ps.OutboundCount -= n
	if ps.OutboundCount < 0 {
		ps.OutboundCount = 0
	}
	m[userID] = ps
	return s.writePeerStatesLocked(safe, m)
}

// ResolveContextToken returns the unified token for a peer (map then peer-state).
func (s *Store) ResolveContextToken(accountID, userID string) string {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens[safe] == nil {
		s.tokens[safe] = make(map[string]string)
		if disk, err := s.readContextTokensLocked(safe); err == nil {
			for k, v := range disk {
				s.tokens[safe][k] = v
			}
		}
	}
	if t := s.tokens[safe][userID]; t != "" {
		return t
	}
	m, err := s.readPeerStatesLocked(safe)
	if err != nil {
		return ""
	}
	return m[userID].ContextToken
}
