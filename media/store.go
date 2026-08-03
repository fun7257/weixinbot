package media

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Store persists decrypted inbound media under a root directory.
type Store struct {
	Root     string
	MaxBytes int64
	mu       sync.Mutex
	seq      int
}

// NewStore creates a media store under root (created if needed).
func NewStore(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("media: empty store root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: abs, MaxBytes: DefaultMaxBytes}, nil
}

// Save writes buf under accountID/subdir with a generated name based on fileName/MIME.
func (s *Store) Save(accountID, fileName string, buf []byte) (string, error) {
	if s == nil {
		return "", fmt.Errorf("media: nil store")
	}
	max := s.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}
	if int64(len(buf)) > max {
		return "", fmt.Errorf("media: payload %d exceeds MaxBytes %d", len(buf), max)
	}
	safe := sanitizeComponent(accountID)
	if safe == "" {
		return "", fmt.Errorf("media: invalid accountID")
	}
	name := filepath.Base(fileName)
	if name == "" || name == "." || name == ".." {
		name = "blob.bin"
	}
	name = sanitizeComponent(name)
	if name == "" {
		name = "blob.bin"
	}

	s.mu.Lock()
	s.seq++
	n := s.seq
	s.mu.Unlock()

	dir := filepath.Join(s.Root, safe, "inbound")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%06d_%s", n, name))
	absRoot, err := filepath.Abs(s.Root)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	sep := string(filepath.Separator)
	if absPath != absRoot && !strings.HasPrefix(absPath, absRoot+sep) {
		return "", fmt.Errorf("media: path escapes store root")
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizeComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "..") {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.', r == '@':
			return r
		default:
			return '_'
		}
	}, s)
	s = strings.Trim(s, ".")
	if s == "" || s == "." || s == ".." {
		return ""
	}
	return s
}
