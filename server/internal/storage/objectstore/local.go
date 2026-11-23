package objectstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// LocalStore persists objects under a root directory (shared PVC / NFS or single-node disk).
type LocalStore struct {
	root string
}

// NewLocal creates a filesystem-backed ObjectStore under root.
func NewLocal(root string) (*LocalStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("local object store root directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &LocalStore{root: abs}, nil
}

func (l *LocalStore) resolve(key string) (string, error) {
	raw := strings.TrimSpace(key)
	if raw == "" || strings.Contains(raw, "..") || strings.Contains(raw, "\\") || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	clean := filepath.ToSlash(filepath.Clean("/" + raw))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." || strings.Contains(clean, "..") {
		return "", fmt.Errorf("invalid object key %q", key)
	}
	full := filepath.Join(l.root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(l.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("object key escapes root: %q", key)
	}
	return full, nil
}

func (l *LocalStore) PutObject(_ context.Context, key string, data []byte, _ string) error {
	p, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (l *LocalStore) GetObject(_ context.Context, key string) ([]byte, bool, error) {
	p, err := l.resolve(key)
	if err != nil {
		return nil, false, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

func (l *LocalStore) DeleteObject(_ context.Context, key string) error {
	p, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// MemoryStore is a concurrency-safe in-memory ObjectStore for unit testing and ephemeral dev runs.
type MemoryStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// NewMemory creates an empty in-memory ObjectStore.
func NewMemory() *MemoryStore {
	return &MemoryStore{data: make(map[string][]byte)}
}

func (m *MemoryStore) PutObject(_ context.Context, key string, data []byte, _ string) error {
	cp := make([]byte, len(data))
	copy(cp, data)
	m.mu.Lock()
	m.data[key] = cp
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) GetObject(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.RLock()
	v, ok := m.data[key]
	m.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, true, nil
}

func (m *MemoryStore) DeleteObject(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.data, key)
	m.mu.Unlock()
	return nil
}
