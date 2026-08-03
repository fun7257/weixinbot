// Package state holds durable session glue for the iLink bot protocol:
// credentials, get_updates_buf cursor, and per-peer context_token.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sanitizeAccountID maps an account id to a single path-safe component under accounts/.
// Rejects empty ids and any value that would escape the store root (e.g. "../x").
func sanitizeAccountID(accountID string) (string, error) {
	id := strings.TrimSpace(accountID)
	if id == "" {
		return "", fmt.Errorf("state: empty accountID")
	}
	// Disallow separators and parent traversal before any join.
	if strings.Contains(id, "..") || strings.ContainsAny(id, `/\`) || strings.ContainsRune(id, 0) {
		return "", fmt.Errorf("state: invalid accountID %q", accountID)
	}
	// Normalize to a conservative filename (aligned with host normalizeAccountId spirit).
	var b strings.Builder
	b.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == '@':
			b.WriteByte('-')
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	out = strings.Trim(out, ".")
	if out == "" || out == "." || out == ".." {
		return "", fmt.Errorf("state: invalid accountID %q", accountID)
	}
	// filepath.Base defense-in-depth: must remain a single component.
	if filepath.Base(out) != out {
		return "", fmt.Errorf("state: invalid accountID %q", accountID)
	}
	return out, nil
}

// accountPath returns an absolute path under accountsDir for the given suffix
// (e.g. ".json", ".sync.json") and verifies it does not escape the accounts root.
func (s *Store) accountPath(accountID, suffix string) (string, error) {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return "", err
	}
	dir := s.accountsDir()
	path := filepath.Join(dir, safe+suffix)
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	sep := string(filepath.Separator)
	if absPath != absDir && !strings.HasPrefix(absPath, absDir+sep) {
		return "", fmt.Errorf("state: path escapes store root")
	}
	return path, nil
}

// Account holds bot credentials persisted after login (or injected in tests).
type Account struct {
	Token     string    `json:"token,omitempty"`
	BaseURL   string    `json:"baseUrl,omitempty"`
	UserID    string    `json:"userId,omitempty"`
	CDNBase   string    `json:"cdnBaseUrl,omitempty"`
	SavedAt   time.Time `json:"savedAt,omitempty"`
}

// Store is a filesystem-backed multi-account state root.
// Layout:
//
//	{root}/accounts.json
//	{root}/accounts/{accountID}.json
//	{root}/accounts/{accountID}.sync.json
//	{root}/accounts/{accountID}.context-tokens.json
type Store struct {
	root string
	mu   sync.Mutex
	// in-memory context tokens: accountID -> userID -> token
	tokens map[string]map[string]string
}

// NewStore creates a store under root (created if missing).
func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("state: empty root")
	}
	if err := os.MkdirAll(filepath.Join(root, "accounts"), 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		root:   root,
		tokens: make(map[string]map[string]string),
	}
	return s, nil
}

func (s *Store) accountsDir() string {
	return filepath.Join(s.root, "accounts")
}

func (s *Store) indexPath() string {
	return filepath.Join(s.root, "accounts.json")
}

// ListAccountIDs returns registered account IDs.
func (s *Store) ListAccountIDs() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// RegisterAccountID appends a sanitized accountID to the index if not present.
func (s *Store) RegisterAccountID(accountID string) error {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	if raw, err := os.ReadFile(s.indexPath()); err == nil {
		_ = json.Unmarshal(raw, &ids)
	}
	for _, id := range ids {
		if id == safe {
			return nil
		}
	}
	ids = append(ids, safe)
	return writeJSON(s.indexPath(), ids)
}

// SaveAccount merges and writes account credentials.
func (s *Store) SaveAccount(accountID string, acc Account) error {
	path, err := s.accountPath(accountID, ".json")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := Account{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &existing)
	}
	if acc.Token != "" {
		existing.Token = acc.Token
		existing.SavedAt = time.Now().UTC()
	}
	if acc.BaseURL != "" {
		existing.BaseURL = acc.BaseURL
	}
	if acc.UserID != "" {
		existing.UserID = acc.UserID
	}
	if acc.CDNBase != "" {
		existing.CDNBase = acc.CDNBase
	}
	return writeJSON(path, existing)
}

// LoadAccount reads account credentials.
func (s *Store) LoadAccount(accountID string) (Account, error) {
	path, err := s.accountPath(accountID, ".json")
	if err != nil {
		return Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return Account{}, err
	}
	var acc Account
	if err := json.Unmarshal(raw, &acc); err != nil {
		return Account{}, err
	}
	return acc, nil
}

// --- sync cursor ---

type syncFile struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

// LoadSyncBuf returns the persisted get_updates_buf (empty if none).
func (s *Store) LoadSyncBuf(accountID string) (string, error) {
	path, err := s.accountPath(accountID, ".sync.json")
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var sf syncFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return "", err
	}
	return sf.GetUpdatesBuf, nil
}

// SaveSyncBuf persists get_updates_buf.
func (s *Store) SaveSyncBuf(accountID, buf string) error {
	path, err := s.accountPath(accountID, ".sync.json")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeJSON(path, syncFile{GetUpdatesBuf: buf})
}

// --- context tokens ---

// SetContextToken stores the conversation context_token for a peer under an account.
// Does not reset outbound counters (use TouchInbound for inbound).
func (s *Store) SetContextToken(accountID, userID, token string) error {
	if userID == "" || token == "" {
		return nil
	}
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokens[safe] == nil {
		s.tokens[safe] = make(map[string]string)
	}
	// load disk if first touch
	if len(s.tokens[safe]) == 0 {
		if disk, err := s.readContextTokensLocked(safe); err == nil {
			for k, v := range disk {
				s.tokens[safe][k] = v
			}
		}
	}
	s.tokens[safe][userID] = token
	if err := s.writeContextTokensLocked(safe); err != nil {
		return err
	}
	return s.setPeerContextTokenLocked(safe, userID, token)
}

// GetContextToken returns a previously stored context_token.
func (s *Store) GetContextToken(accountID, userID string) string {
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
	return s.tokens[safe][userID]
}

// RestoreContextTokens loads disk tokens into memory for an account.
func (s *Store) RestoreContextTokens(accountID string) error {
	safe, err := sanitizeAccountID(accountID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	disk, err := s.readContextTokensLocked(safe)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	s.tokens[safe] = disk
	return nil
}

func (s *Store) readContextTokensLocked(safeAccountID string) (map[string]string, error) {
	// safeAccountID must already be sanitized; still go through accountPath for escape check.
	path, err := s.accountPath(safeAccountID, ".context-tokens.json")
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = make(map[string]string)
	}
	return m, nil
}

func (s *Store) writeContextTokensLocked(safeAccountID string) error {
	path, err := s.accountPath(safeAccountID, ".context-tokens.json")
	if err != nil {
		return err
	}
	m := s.tokens[safeAccountID]
	if m == nil {
		m = make(map[string]string)
	}
	return writeJSON(path, m)
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	// Atomic replace: write temp in same dir then rename.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
