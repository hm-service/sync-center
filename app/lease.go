package app

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type Lease struct {
	ID          string
	NamespaceID int64
	ExpiresAt   time.Time
}

type LeaseStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[int64]*Lease
}

func NewLeaseStore(ttl time.Duration) *LeaseStore {
	return &LeaseStore{ttl: ttl, items: make(map[int64]*Lease)}
}

func (s *LeaseStore) purgeLocked(now time.Time) {
	for id, lease := range s.items {
		if !lease.ExpiresAt.After(now) {
			delete(s.items, id)
		}
	}
}

// Acquire 每个 namespace 同时只允许一个未过期租约。
func (s *LeaseStore) Acquire(namespaceID int64) (*Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeLocked(now)
	if _, ok := s.items[namespaceID]; ok {
		return nil, false
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, false
	}
	lease := &Lease{
		ID:          hex.EncodeToString(raw),
		NamespaceID: namespaceID,
		ExpiresAt:   now.Add(s.ttl),
	}
	s.items[namespaceID] = lease
	return lease, true
}

func (s *LeaseStore) Peek(id string, namespaceID int64) (*Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	lease, ok := s.items[namespaceID]
	if !ok || lease.ID != id {
		return nil, false
	}
	return lease, true
}

func (s *LeaseStore) Heartbeat(id string, namespaceID int64) (*Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeLocked(now)
	lease, ok := s.items[namespaceID]
	if !ok || lease.ID != id {
		return nil, false
	}
	lease.ExpiresAt = now.Add(s.ttl)
	return lease, true
}

func (s *LeaseStore) Release(id string, namespaceID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.items[namespaceID]
	if !ok || lease.ID != id {
		return false
	}
	delete(s.items, namespaceID)
	return true
}
