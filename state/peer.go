package state

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// PeerState holds per-conversation outbound policy metadata and the latest context_token.
type PeerState struct {
	ContextToken  string    `json:"contextToken,omitempty"`
	LastInboundAt time.Time `json:"lastInboundAt,omitempty"`
	OutboundCount int       `json:"outboundCount,omitempty"`
}

// GetPeer returns durable peer state (zero value if missing).
func (s *Store) GetPeer(accountID, userID string) (PeerState, error) {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return PeerState{}, err
	}
	if userID == "" {
		return PeerState{}, fmt.Errorf("state: empty userID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readPeerStatesLocked(safe)
	if err != nil {
		if os.IsNotExist(err) {
			return PeerState{}, nil
		}
		return PeerState{}, err
	}
	return m[userID], nil
}

// TouchInbound records an inbound message: updates token, lastInboundAt, resets outbound count.
func (s *Store) TouchInbound(accountID, userID, contextToken string, at time.Time) error {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return err
	}
	if userID == "" {
		return fmt.Errorf("state: empty userID")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readPeerStatesLocked(safe)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if m == nil {
		m = make(map[string]PeerState)
	}
	ps := m[userID]
	if contextToken != "" {
		ps.ContextToken = contextToken
	}
	ps.LastInboundAt = at
	ps.OutboundCount = 0
	m[userID] = ps
	if err := s.writePeerStatesLocked(safe, m); err != nil {
		return err
	}
	// Keep legacy context-token map in sync for GetContextToken callers.
	if contextToken != "" {
		if s.tokens[safe] == nil {
			s.tokens[safe] = make(map[string]string)
			if disk, err := s.readContextTokensLocked(safe); err == nil {
				for k, v := range disk {
					s.tokens[safe][k] = v
				}
			}
		}
		s.tokens[safe][userID] = contextToken
		if err := s.writeContextTokensLocked(safe); err != nil {
			return err
		}
	}
	return nil
}

// IncrOutbound increments the outbound counter for a peer since last inbound.
func (s *Store) IncrOutbound(accountID, userID string) (int, error) {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return 0, err
	}
	if userID == "" {
		return 0, fmt.Errorf("state: empty userID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.readPeerStatesLocked(safe)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	if m == nil {
		m = make(map[string]PeerState)
	}
	ps := m[userID]
	ps.OutboundCount++
	m[userID] = ps
	if err := s.writePeerStatesLocked(safe, m); err != nil {
		return 0, err
	}
	return ps.OutboundCount, nil
}

// ResetOutboundOnInbound is an alias for TouchInbound without changing token when token is empty.
func (s *Store) ResetOutboundOnInbound(accountID, userID string, at time.Time) error {
	return s.TouchInbound(accountID, userID, "", at)
}

// SetContextToken stores the conversation context_token for a peer under an account.
// Also updates peer-state.json ContextToken without resetting outbound counters.
func (s *Store) setPeerContextTokenLocked(safe, userID, token string) error {
	m, err := s.readPeerStatesLocked(safe)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if m == nil {
		m = make(map[string]PeerState)
	}
	ps := m[userID]
	ps.ContextToken = token
	m[userID] = ps
	return s.writePeerStatesLocked(safe, m)
}

func (s *Store) readPeerStatesLocked(safeAccountID string) (map[string]PeerState, error) {
	path, err := s.accountPath(safeAccountID, ".peer-state.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]PeerState
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = make(map[string]PeerState)
	}
	return m, nil
}

func (s *Store) writePeerStatesLocked(safeAccountID string, m map[string]PeerState) error {
	path, err := s.accountPath(safeAccountID, ".peer-state.json")
	if err != nil {
		return err
	}
	if m == nil {
		m = make(map[string]PeerState)
	}
	return writeJSON(path, m)
}

// DeleteAccount removes account credential, sync, context-tokens, and peer-state files.
func (s *Store) DeleteAccount(accountID string) error {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, suffix := range []string{".json", ".sync.json", ".context-tokens.json", ".peer-state.json"} {
		path, err := s.accountPath(safe, suffix)
		if err != nil {
			return err
		}
		_ = os.Remove(path)
	}
	delete(s.tokens, safe)
	// drop from index
	ids := []string{}
	if raw, err := os.ReadFile(s.indexPath()); err == nil {
		_ = json.Unmarshal(raw, &ids)
	}
	out := ids[:0]
	for _, id := range ids {
		if id != safe {
			out = append(out, id)
		}
	}
	return writeJSON(s.indexPath(), out)
}

// ClearStaleAccountsForUserID deletes other accounts that share the same WeChat userId.
func (s *Store) ClearStaleAccountsForUserID(keepAccountID, userID string) error {
	if userID == "" {
		return nil
	}
	ids, err := s.ListAccountIDs()
	if err != nil {
		return err
	}
	keepSafe, err := sanitizeAccountID(keepAccountID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == keepSafe {
			continue
		}
		acc, err := s.LoadAccount(id)
		if err != nil {
			continue
		}
		if acc.UserID == userID {
			_ = s.DeleteAccount(id)
		}
	}
	return nil
}
