//go:build linux

package auth

import "sync"

// MemoryKeyring is an in-memory Secret Service stand-in for tests. It is
// never selected unless a test installs it with UseKeyringForTest.
type MemoryKeyring struct {
	mu sync.Mutex
	// Unavailable makes every call fail like an unreachable Secret Service.
	Unavailable bool
	items       map[key][]byte
}

func (m *MemoryKeyring) Len() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.items) }

func (m *MemoryKeyring) Get(k key) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return nil, ssUnavailable("test keyring unavailable")
	}
	v, ok := m.items[k]
	if !ok {
		return nil, ErrNoCredentials
	}
	return append([]byte(nil), v...), nil
}
func (m *MemoryKeyring) Put(k key, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return ssUnavailable("test keyring unavailable")
	}
	if m.items == nil {
		m.items = map[key][]byte{}
	}
	m.items[k] = append([]byte(nil), v...)
	return nil
}
func (m *MemoryKeyring) Remove(k key) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return ssUnavailable("test keyring unavailable")
	}
	delete(m.items, k)
	return nil
}
func (m *MemoryKeyring) probe() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Unavailable {
		return ssUnavailable("test keyring unavailable")
	}
	return nil
}

// UseKeyringForTest swaps the Secret Service backend and returns a restore
// function. Tests only.
func UseKeyringForTest(m *MemoryKeyring) (restore func()) {
	prev := systemKeyring
	systemKeyring = m
	return func() { systemKeyring = prev }
}
